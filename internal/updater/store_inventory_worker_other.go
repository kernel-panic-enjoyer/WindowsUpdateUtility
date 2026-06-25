//go:build !windows

package updater

const storeInventoryWorkerFlag = "--store-inventory-worker"

func runStoreInventoryWorkerFromArgs() int {
	return 2
}
