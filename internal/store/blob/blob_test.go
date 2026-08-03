package blob

import "testing"

func TestInMemory_Contract(t *testing.T) {
	t.Parallel()
	RunContractSuite(t, NewInMemory())
}
