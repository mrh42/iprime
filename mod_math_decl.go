// Declarations for assembler routines

// +build amd64

package main

//go:noescape

func mod_sub(x, y uint64) (ret uint64)

//go:noescape

func mod_add(x, y uint64) (ret uint64)

//go:noescape

func mod_reduce(b, a uint64) (ret uint64)

//go:noescape

func mod_mul(x, y uint64) (ret uint64)

//go:noescape

func mod_sqr(x uint64) (ret uint64)
