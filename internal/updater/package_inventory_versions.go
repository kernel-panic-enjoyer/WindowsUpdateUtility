package updater

func mergeUpdateVersions(target map[string]string, updates map[string]string) {
	for key, available := range updates {
		target[key] = available
	}
}

func mergeWingetStoreUpdateVersions(target map[string]string, updates map[string]string) {
	for key, available := range updates {
		manager, _, err := splitPackageKey(key)
		if err == nil && manager == managerStore {
			target[key] = available
		}
	}
}
