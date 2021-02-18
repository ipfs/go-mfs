// package mfs implements an in memory model of a mutable IPFS filesystem.
// TODO: Develop on this line (and move it to `doc.go`).

package mfs

import (
	"context"
	"fmt"
	"time"

	dag "github.com/ipfs/go-merkledag"
	ft "github.com/ipfs/go-unixfs"

	ipld "github.com/ipfs/go-ipld-format"
)

// The information that an MFS `Directory` has about its children
// when updating one of its entries: when a child mutates it signals
// its parent directory to update its entry (under `Name`) with the
// new content (in `Node`).
type child struct {
	Name string
	Node ipld.Node
}

// This interface represents the basic property of MFS directories of updating
// children entries with modified content. Implemented by both the MFS
// `Directory` and `Root` (which is basically a `Directory` with republishing
// support).
//
// TODO: What is `fullsync`? (unnamed `bool` argument)
// TODO: There are two types of persistence/flush that need to be
// distinguished here, one at the DAG level (when I store the modified
// nodes in the DAG service) and one in the UnixFS/MFS level (when I modify
// the entry/link of the directory that pointed to the modified node).
type parent interface {
	// Method called by a child to its parent to signal to update the content
	// pointed to in the entry by that child's name. The child sends its own
	// information in the `child` structure. As modifying a directory entry
	// entails modifying its contents the parent will also call *its* parent's
	// `updateChildEntry` to update the entry pointing to the new directory,
	// this mechanism is in turn repeated until reaching the `Root`.
	updateChildEntry(c child) error
}

type NodeType int

const (
	TFile NodeType = iota
	TDir
)

const (
	repubQuick   = time.Millisecond * 300
	repubLong    = time.Second * 3
	closeTimeout = time.Second
)

// FSNode abstracts the `Directory` and `File` structures, it represents
// any child node in the MFS (i.e., all the nodes besides the `Root`). It
// is the counterpart of the `parent` interface which represents any
// parent node in the MFS (`Root` and `Directory`).
// (Not to be confused with the `unixfs.FSNode`.)
type FSNode interface {
	GetNode() (ipld.Node, error)

	Flush() error
	Type() NodeType
}

// IsDir checks whether the FSNode is dir type
func IsDir(fsn FSNode) bool {
	return fsn.Type() == TDir
}

// IsFile checks whether the FSNode is file type
func IsFile(fsn FSNode) bool {
	return fsn.Type() == TFile
}

// Root represents the root of a filesystem tree.
type Root struct {
	// Root directory of the MFS layout.
	dir *Directory

	repub *Republisher
}

// NewRoot creates a new Root and starts up a republisher routine for it.
func NewRoot(ds ipld.DAGService, node *dag.ProtoNode, pf PubFunc) (*Root, error) {
	root := &Root{}

	fsn, err := ft.FSNodeFromBytes(node.Data())
	if err != nil {
		return nil, fmt.Errorf("node data was not unixfs node: %s", err)
	}

	switch fsn.Type() {
	case ft.TDirectory, ft.THAMTShard:
		root.dir, err = NewDirectory(node.String(), node, root, ds)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("root must be a unixfs directory, not type: %s", fsn.Type())
	}

	if pf != nil {
		root.repub = NewRepublisher(pf, repubQuick, repubLong, node.Cid())
	}

	return root, nil
}

// GetDirectory returns the root directory.
func (kr *Root) GetDirectory() *Directory {
	return kr.dir
}

// Flush signals that an update has occurred since the last publish,
// and updates the Root republisher.
// TODO: We are definitely abusing the "flush" terminology here.
func (kr *Root) Flush() error {
	nd, err := kr.GetDirectory().GetNode()
	if err != nil {
		return err
	}

	if kr.repub != nil {
		kr.repub.Update(nd.Cid())
	}
	return nil
}

// FlushMemFree flushes the root directory and then uncaches all of its links.
// This has the effect of clearing out potentially stale references and allows
// them to be garbage collected.
// CAUTION: Take care not to ever call this while holding a reference to any
// child directories. Those directories will be bad references and using them
// may have unintended racy side effects.
// A better implemented mfs system (one that does smarter internal caching and
// refcounting) shouldnt need this method.
// TODO: Review the motivation behind this method once the cache system is
// refactored.
func (kr *Root) FlushMemFree() error {
	dir := kr.GetDirectory()

	if err := dir.Flush(); err != nil {
		return err
	}

	dir.lock.Lock()
	defer dir.lock.Unlock()

	dir.entriesCache = make(map[string]FSNode)

	return nil
}

// updateChildEntry implements the `parent` interface, and signals
// to the publisher that there are changes ready to be published.
// This is the only thing that separates a `Root` from a `Directory`.
// TODO: Evaluate merging both.
func (kr *Root) updateChildEntry(c child) error {
	dir := kr.GetDirectory()

	dir.lock.Lock()

	err := dir.dagService.Add(context.Background(), c.Node)
	if err != nil {
		dir.lock.Unlock()
		return err
	}

	dir.lock.Unlock()

	if kr.repub != nil {
		kr.repub.Update(c.Node.Cid())
	}
	return nil
}

func (kr *Root) Close() error {
	if kr.repub != nil {
		defer kr.repub.Close()
	}

	nd, err := kr.GetDirectory().GetNode()
	if err != nil {
		return err
	}

	if kr.repub != nil {
		kr.repub.Update(nd.Cid())

		// Wait to finish publishing
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		err = kr.repub.WaitPub(ctx)
		if err != nil {
			return err
		}
	}

	return nil
}
