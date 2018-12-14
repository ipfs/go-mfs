package mfs

import (
	"context"
	"testing"
)

func TestMv(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ds, rt := setupRoot(ctx, t)

	// mkdir dir
	dirs := []string{"/dir-a", "/dir-b", "/dir-c", "/dir-a/sub-a"}
	for _, dir := range dirs {
		err := Mkdir(rt, dir, MkdirOpts{Mkparents: true, Flush: true})
		if err != nil {
			t.Fatal(err)
		}
	}

	// create some files
	nd := getRandFile(t, ds, 1000)
	err := rt.GetDirectory().AddChild("file-a", nd)
	if err != nil {
		t.Fatal(err)
	}
	err = rt.GetDirectory().AddChild("file-b", nd)
	if err != nil {
		t.Fatal(err)
	}

	// mv parent dir to child dir
	err = Mv(rt, "/dir-a", "/dir-a/sub-a")
	if err != errMvParentDir {
		t.Fatal(err)
	}

	err = Mv(rt, "/", "/dir-a")
	if err != errMvParentDir {
		t.Fatal(err)
	}

	// src path ends with '/' is not a directory
	err = Mv(rt, "/file-a/", "/dir-a")
	if err != errInvalidDirPath {
		t.Fatal(err)
	}

	err = Mv(rt, "/dir-b", "/file-a")
	if err != errMvDirToFile {
		t.Fatal(err)
	}

	// fix #5346
	err = Mv(rt, "/file-a", "/dir-a")
	if err != nil {
		t.Fatal(err)
	}

	_, err = Lookup(rt, "/dir-a/file-a")
	if err != nil {
		t.Fatal(err)
	}

	err = Mv(rt, "/file-b", "/dir-a/")
	if err != nil {
		t.Fatal(err)
	}

	_, err = Lookup(rt, "/dir-a/file-b")
	if err != nil {
		t.Fatal(err)
	}
}
