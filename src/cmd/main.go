// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

// Command snapback runs the Snapback controller-manager: it watches Velero
// Backups and replicates completed backups to a peer cluster over a co-located
// peat-node sidecar. See docs/DESIGN.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	snapbackv1alpha1 "github.com/defenseunicorns/snapback/api/v1alpha1"
	velerov1 "github.com/defenseunicorns/snapback/api/velero/v1"
	"github.com/defenseunicorns/snapback/internal/controller"
	"github.com/defenseunicorns/snapback/internal/manifest"
	"github.com/defenseunicorns/snapback/internal/objstore"
)

const (
	roleSource      = "source"
	roleDestination = "destination"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(snapbackv1alpha1.AddToScheme(scheme))
	utilruntime.Must(velerov1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool
	var role string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.StringVar(&role, "role", roleSource, "Snapback role: source | destination.")

	// Destination-role configuration (ignored when role=source).
	var (
		peatAddress        string
		inboxPath          string
		manifestCollection string
		pollInterval       time.Duration
		destEndpoint       string
		destRegion         string
		destBucket         string
		destPrefix         string
		destForcePathStyle bool
		destInsecureTLS    bool
		destCredsFile      string
		destCredsProfile   string
		sourceEndpointID   string
		sourceAddresses    string
		sourceRelayURL     string
	)
	flag.StringVar(&peatAddress, "peat-address", "localhost:50051", "Local peat sidecar gRPC address (destination).")
	flag.StringVar(&inboxPath, "inbox-path", "/var/lib/peat/inbox", "peat attachment inbox mount path (destination).")
	flag.StringVar(&manifestCollection, "manifest-collection", manifest.Collection, "peat document collection for replication manifests (destination).")
	flag.DurationVar(&pollInterval, "manifest-poll-interval", 30*time.Second, "How often to poll the manifest collection (destination).")
	flag.StringVar(&destEndpoint, "dest-endpoint", "", "Destination object-store endpoint, e.g. http://minio.uds-dev-stack.svc:9000 (destination).")
	flag.StringVar(&destRegion, "dest-region", "", "Destination object-store region (destination).")
	flag.StringVar(&destBucket, "dest-bucket", "", "Destination object-store bucket (destination).")
	flag.StringVar(&destPrefix, "dest-prefix", "", "Destination object-store key prefix; the destination Velero BSL must use this same prefix (destination).")
	flag.BoolVar(&destForcePathStyle, "dest-s3-force-path-style", true, "Use path-style S3 addressing for the destination store (MinIO).")
	flag.BoolVar(&destInsecureTLS, "dest-insecure-tls", false, "Skip TLS verification for the destination store (destination).")
	flag.StringVar(&destCredsFile, "dest-credentials-file", "", "Path to an AWS shared-credentials file for the destination store (destination).")
	flag.StringVar(&destCredsProfile, "dest-credentials-profile", "", "Profile within the destination credentials file (destination).")
	flag.StringVar(&sourceEndpointID, "source-endpoint-id", "", "Source peat endpoint id for reverse peering; empty = bootstrap manually (destination).")
	flag.StringVar(&sourceAddresses, "source-addresses", "", "Comma-separated source peat UDP host:port addresses (destination).")
	flag.StringVar(&sourceRelayURL, "source-relay-url", "", "Optional source peat relay URL (destination).")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	if role != roleSource && role != roleDestination {
		setupLog.Error(fmt.Errorf("invalid role %q", role), "role must be \"source\" or \"destination\"")
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "snapback.uds.dev",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	switch role {
	case roleSource:
		// Source: watch Velero Backups and replicate completed ones over peat.
		if err := (&controller.BackupReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Backup")
			os.Exit(1)
		}
		if err := (&controller.BackupReplicationReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "BackupReplication")
			os.Exit(1)
		}
	case roleDestination:
		// Destination: poll the manifest collection and reconstruct backups into
		// the destination object store. Co-located with the peat receiver.
		importOpts, err := buildImportOptions(
			peatAddress, inboxPath, manifestCollection, pollInterval,
			destEndpoint, destRegion, destBucket, destPrefix, destForcePathStyle, destInsecureTLS,
			destCredsFile, destCredsProfile, sourceEndpointID, sourceAddresses, sourceRelayURL,
		)
		if err != nil {
			setupLog.Error(err, "unable to build destination import options")
			os.Exit(1)
		}
		if err := (&controller.BackupImportReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
			Opts:   importOpts,
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "BackupImport")
			os.Exit(1)
		}
		if err := mgr.Add(&controller.ManifestBridge{Client: mgr.GetClient(), Opts: importOpts}); err != nil {
			setupLog.Error(err, "unable to add manifest bridge")
			os.Exit(1)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager", "role", role)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// buildImportOptions assembles the destination-role import configuration from
// flags, resolving the destination object-store credentials from a Velero-style
// AWS shared-credentials file. The destination prefix should match the source
// BSL prefix so Velero's nested layout (<prefix>/backups/<name>/...) lines up.
func buildImportOptions(
	peatAddress, inboxPath, manifestCollection string, pollInterval time.Duration,
	endpoint, region, bucket, prefix string, forcePathStyle, insecureTLS bool,
	credsFile, credsProfile, sourceEndpointID, sourceAddresses, sourceRelayURL string,
) (controller.ImportOptions, error) {
	if endpoint == "" {
		return controller.ImportOptions{}, fmt.Errorf("--dest-endpoint is required for role=destination")
	}
	if bucket == "" {
		return controller.ImportOptions{}, fmt.Errorf("--dest-bucket is required for role=destination")
	}
	cfg := objstore.Config{
		Endpoint:       endpoint,
		Region:         region,
		Bucket:         bucket,
		Prefix:         prefix,
		ForcePathStyle: forcePathStyle,
		InsecureTLS:    insecureTLS,
	}
	if credsFile != "" {
		credBytes, err := os.ReadFile(credsFile)
		if err != nil {
			return controller.ImportOptions{}, fmt.Errorf("read dest credentials %q: %w", credsFile, err)
		}
		ak, sk, token, err := objstore.ParseAWSCredentials(credBytes, credsProfile)
		if err != nil {
			return controller.ImportOptions{}, fmt.Errorf("parse dest credentials: %w", err)
		}
		cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken = ak, sk, token
	}
	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		ns = "snapback"
	}
	return controller.ImportOptions{
		PeatAddress:        peatAddress,
		InboxRoot:          inboxPath,
		ManifestCollection: manifestCollection,
		Namespace:          ns,
		PollInterval:       pollInterval,
		DestStore:          cfg,
		SourceEndpointID:   sourceEndpointID,
		SourceAddresses:    splitCSV(sourceAddresses),
		SourceRelayURL:     sourceRelayURL,
	}, nil
}

// splitCSV splits a comma-separated list, trimming spaces and dropping empties.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
