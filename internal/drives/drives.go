package drives

// Drive describes a logical drive exposed by the operating system.
type Drive struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	Accessible bool   `json:"accessible"`
}

const (
	TypeFixed     = "fixed"
	TypeRemovable = "removable"
	TypeNetwork   = "network"
	TypeCDROM     = "cdrom"
	TypeRAMDisk   = "ramdisk"
	TypeUnknown   = "unknown"
)
