// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"sync"
	"testing"

	benchv1alpha1 "github.com/lpasquali/rune-operator/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var (
	sharedControllersTestScheme     *runtime.Scheme
	sharedControllersTestSchemeOnce sync.Once
	sharedControllersTestSchemeErr  error
)

// controllersTestScheme returns a shared scheme with api/v1alpha1 and core types
// registered for controller unit tests (read-only after init).
func controllersTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sharedControllersTestSchemeOnce.Do(func() {
		s := runtime.NewScheme()
		if err := benchv1alpha1.AddToScheme(s); err != nil {
			sharedControllersTestSchemeErr = err
			return
		}
		if err := corev1.AddToScheme(s); err != nil {
			sharedControllersTestSchemeErr = err
			return
		}
		if err := batchv1.AddToScheme(s); err != nil {
			sharedControllersTestSchemeErr = err
			return
		}
		sharedControllersTestScheme = s
	})
	if sharedControllersTestSchemeErr != nil {
		t.Fatalf("controllers test scheme: %v", sharedControllersTestSchemeErr)
	}
	return sharedControllersTestScheme
}
