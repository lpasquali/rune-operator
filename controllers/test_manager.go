package controllers
import (
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
)
func buildTestManager() (ctrl.Manager, error) {
	return ctrl.NewManager(&rest.Config{}, ctrl.Options{})
}
