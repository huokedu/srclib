package store

import "testing"

func TestFSUnitStore(t *testing.T) {
	useIndexedStore = false
	testUnitStore(t, func() unitStoreImporter {
		return &fsUnitStore{fs: newTestFS()}
	})
}

func TestFSTreeStore(t *testing.T) {
	useIndexedStore = false
	testTreeStore(t, func() treeStoreImporter {
		return newFSTreeStore(newTestFS())
	})
}

func TestFSRepoStore(t *testing.T) {
	useIndexedStore = false
	testRepoStore(t, func() RepoStoreImporter {
		return NewFSRepoStore(newTestFS())
	})
}

func TestFSMultiRepoStore(t *testing.T) {
	useIndexedStore = false
	testMultiRepoStore(t, func() MultiRepoStoreImporter {
		return NewFSMultiRepoStore(newTestFS(), nil)
	})
}

func TestFSMultiRepoStore_EvenlyDistributedRepoPaths(t *testing.T) {
	useIndexedStore = false
	testMultiRepoStore(t, func() MultiRepoStoreImporter {
		return NewFSMultiRepoStore(newTestFS(), &FSMultiRepoStoreConf{RepoPaths: &EvenlyDistributedRepoPaths{}})
	})
}