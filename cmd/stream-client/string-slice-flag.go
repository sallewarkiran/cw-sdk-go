package main

import "fmt"

type stringSlice []string

func (ss *stringSlice) String() string {
	return fmt.Sprintf("%v", *ss)
}

func (ss *stringSlice) Set(value string) error {
	*ss = append(*ss, value)
	return nil
}
