//go:build !windows

package main

func listBluetoothPhones() (map[string]any, error) {
	return map[string]any{
		"ok":      false,
		"message": "No paired phone was found.",
		"devices": []any{},
	}, nil
}

func pickFilesForBluetooth() ([]string, error) {
	return nil, nil
}

func startBluetoothSend(id string, rels []string, outside []string) {}

func startBluetoothScan() {}

func stopBluetoothScan() {}
