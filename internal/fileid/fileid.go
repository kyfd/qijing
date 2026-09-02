// Package fileid captures a stable Windows file identity: the volume serial
// number plus the file reference number, together with the metadata that was
// observed alongside it.
//
// 路径会变（重命名、同卷移动），size 和 mtime 可以被伪造得完全一致；
// (volume_serial, file_id) 才是"用户确认的那一个文件"的稳定锚点。
// 整理动作在执行前必须重新打开文件并比对完整身份。
package fileid

import "time"

// Identity is one file's stable identity plus observed metadata. Matching
// requires every field to agree: the reference number proves it is the same
// file object, size and timestamps prove the observed bytes are the ones the
// user looked at.
type Identity struct {
	VolumeSerial uint32    `json:"volume_serial"`
	FileID       uint64    `json:"file_id"`
	Size         int64     `json:"size"`
	ModTime      time.Time `json:"mod_time"`
	CreationTime time.Time `json:"creation_time"`
}

// Matches reports whether two observations describe the same file object
// with the same content metadata. It never treats an empty identity as a
// match: a zero VolumeSerial plus zero FileID means the platform could not
// provide a stable identity and the caller must not claim object identity.
func (i Identity) Matches(other Identity) bool {
	if i.VolumeSerial == 0 && i.FileID == 0 {
		return false
	}
	return i.VolumeSerial == other.VolumeSerial &&
		i.FileID == other.FileID &&
		i.Size == other.Size &&
		i.ModTime.Equal(other.ModTime) &&
		i.CreationTime.Equal(other.CreationTime)
}
