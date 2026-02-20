// Package main is the entrypoint for the workspace operator.
package main

import (
	"flag"
	"os"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
	"workspace-operator/controllers"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(workspacev1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "workspace.devplane.io",
	})
	if err != nil {
		setupLog.Error(err, "Unable to start manager")
		os.Exit(1)
	}

	workspaceImage := os.Getenv("WORKSPACE_IMAGE")
	if workspaceImage == "" {
		workspaceImage = "workspace:latest"
	}
	llmNamespacesRaw := os.Getenv("LLM_NAMESPACES")
	var llmNamespaces []string
	if llmNamespacesRaw != "" {
		for _, ns := range strings.Split(llmNamespacesRaw, ",") {
			if ns = strings.TrimSpace(ns); ns != "" {
				llmNamespaces = append(llmNamespaces, ns)
			}
		}
	}

	// EGRESS_PORTS is an optional comma-separated list of TCP port numbers that
	// workspace pods are allowed to connect to on external IPs (0.0.0.0/0).
	// Example: "22,80,443,8000,11434"
	// When unset the built-in default list (security.DefaultEgressPorts) is used.
	egressPortsRaw := os.Getenv("EGRESS_PORTS")
	var egressPorts []int32
	if egressPortsRaw != "" {
		for _, raw := range strings.Split(egressPortsRaw, ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			p, parseErr := strconv.ParseInt(raw, 10, 32)
			if parseErr != nil || p < 1 || p > 65535 {
				setupLog.Info("Ignoring invalid EGRESS_PORTS entry", "value", raw)
				continue
			}
			egressPorts = append(egressPorts, int32(p))
		}
	}

	if err = (&controllers.WorkspaceReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		WorkspaceImage: workspaceImage,
		LLMNamespaces:  llmNamespaces,
		EgressPorts:    egressPorts,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Unable to create controller", "controller", "Workspace")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Problem running manager")
		os.Exit(1)
	}
}
