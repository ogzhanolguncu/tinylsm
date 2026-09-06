package manifest

import "slices"

type FileMeta struct {
	Level   uint32
	FileNum uint64
}

type VersionEdit struct {
	AddFiles    []FileMeta
	DelFiles    []FileMeta
	NextFileNum uint64
	LastSeq     uint64
}

const NumLevels = 7

type Version struct {
	Files       [NumLevels][]FileMeta
	NextFileNum uint64
	LastSeq     uint64
}

func (v *Version) Apply(e VersionEdit) *Version {
	var files [NumLevels][]FileMeta
	for i := range files {
		files[i] = slices.Clone(v.Files[i])
	}

	for _, f := range e.AddFiles {
		files[f.Level] = append(files[f.Level], f)
	}

	for _, f := range e.DelFiles {
		files[f.Level] = slices.DeleteFunc(files[f.Level], func(fm FileMeta) bool {
			return fm.FileNum == f.FileNum
		})
	}

	nextFileNum := v.NextFileNum
	if e.NextFileNum > 0 {
		nextFileNum = e.NextFileNum
	}

	lastSeq := v.LastSeq
	if e.LastSeq > 0 {
		lastSeq = e.LastSeq
	}

	return &Version{
		Files:       files,
		NextFileNum: nextFileNum,
		LastSeq:     lastSeq,
	}
}
