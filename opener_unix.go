//go:build !windows

package main

import "syscall"

const outputNoFollow = syscall.O_NOFOLLOW
