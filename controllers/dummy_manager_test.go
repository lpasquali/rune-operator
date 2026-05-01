package controllers

import (
	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"testing"
)

func TestSetupWithManager_Success(t *testing.T) {
	scheme := controllersTestScheme(t)
	mgr, err := ctrl.NewManager(&rest.Config{}, ctrl.Options{Scheme: scheme})
	assert.NoError(t, err)

	r := &RuneBenchmarkReconciler{Scheme: scheme}
	err = r.SetupWithManager(mgr)
	assert.NoError(t, err)
}
