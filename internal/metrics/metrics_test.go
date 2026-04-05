// SPDX-License-Identifier: Apache-2.0
package metrics

import "testing"

func TestRegisterIsIdempotent(t *testing.T) {
	Register()
	Register()
}
