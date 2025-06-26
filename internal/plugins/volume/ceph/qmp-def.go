package ceph

type BlockExportResponse struct {
	Data []BlockExportNode `json:"return"`
}

type BlockExportNode struct {
	NodeName     string `json:"node-name"`
	ShuttingDown bool   `json:"shutting-down"`
	Type         string `json:"type"`
	ID           string `json:"id"`
}

type BlockDevicesResponse struct {
	Data []BlockDevice `json:"return"`
}

type BlockDevice struct {
	IOPSRd           int        `json:"iops_rd"`
	IOPSWr           int        `json:"iops_wr"`
	IOPS             int        `json:"iops"`
	BPSRd            int        `json:"bps_rd"`
	BPSWr            int        `json:"bps_wr"`
	BPS              int        `json:"bps"`
	WriteThreshold   int        `json:"write_threshold"`
	DetectZeroes     string     `json:"detect_zeroes"`
	NodeName         string     `json:"node-name"`
	BackingFileDepth int        `json:"backing_file_depth"`
	Drv              string     `json:"drv"`
	RO               bool       `json:"ro"`
	Encrypted        bool       `json:"encrypted"`
	Image            BlockImage `json:"image"`
	File             string     `json:"file"`
	Cache            BlockCache `json:"cache"`
}

type BlockImage struct {
	VirtualSize    int64                `json:"virtual-size"`
	Filename       string               `json:"filename"`
	ClusterSize    int64                `json:"cluster-size"`
	Format         string               `json:"format"`
	DirtyFlag      bool                 `json:"dirty-flag"`
	FormatSpecific FormatSpecificDetail `json:"format-specific"`
}

type FormatSpecificDetail struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"` // empty object, or make a real struct if known
}

type BlockCache struct {
	NoFlush   bool `json:"no-flush"`
	Direct    bool `json:"direct"`
	Writeback bool `json:"writeback"`
}
