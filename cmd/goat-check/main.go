package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/mknyszek/goat"
	"github.com/mknyszek/goat/cmd/internal/spinner"
	"github.com/mknyszek/goat/simulation/toolbox"

	"golang.org/x/exp/mmap"
)

var printFlag *bool = flag.Bool("print", false, "print events as they're seen")

func init() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "Utility that sanity-checks Go allocation traces\n")
		fmt.Fprintf(flag.CommandLine.Output(), "and prints some statistics.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "usage: %s [flags] <allocation-trace-file>\n")
		flag.PrintDefaults()
	}
}

func handleError(err error, usage bool) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	if usage {
		flag.Usage()
	}
	os.Exit(1)
}

func main() {
	flag.Parse()
	if flag.NArg() != 1 {
		handleError(errors.New("incorrect number of arguments"), true)
	}
	r, err := mmap.Open(flag.Arg(0))
	if err != nil {
		handleError(fmt.Errorf("incorrect number of arguments: %v", err), false)
	}
	defer r.Close()
	fmt.Println("Generating parser...")
	p, err := goat.NewParser(r)
	if err != nil {
		handleError(fmt.Errorf("creating parser: %v", err), false)
	}
	fmt.Println("Parsing events...")

	var pMu sync.Mutex
	spinner.Start(func() float64 {
		pMu.Lock()
		prog := p.Progress()
		pMu.Unlock()
		return prog
	}, spinner.Format("Processing... %.4f%%"))

	const maxErrors = 20
	allocs, frees, gcs := 0, 0, 0
	var sanity toolbox.AddressSet
	var reuseWithoutFree []goat.Event
	var doubleFree []goat.Event
	minTicks := ^uint64(0)
	for {
		pMu.Lock()
		ev, err := p.Next()
		pMu.Unlock()
		if err == io.EOF {
			break
		}
		if minTicks == ^uint64(0) {
			minTicks = ev.Timestamp
		}
		if err != nil {
			handleError(fmt.Errorf("parsing events: %v", err), false)
		}
		switch ev.Kind {
		case goat.EventAlloc:
			if *printFlag {
				fmt.Printf("[%d P %d] alloc(%d) @ 0x%x\n", ev.Timestamp-minTicks, ev.P, ev.Size, ev.Address)
			}
			if ok := sanity.Add(ev.Address); !ok {
				reuseWithoutFree = append(reuseWithoutFree, ev)
			}
			allocs++
		case goat.EventFree:
			if *printFlag {
				fmt.Printf("[%d P %d] free @ 0x%x\n", ev.Timestamp-minTicks, ev.P, ev.Address)
			}
			if ok := sanity.Remove(ev.Address); !ok {
				doubleFree = append(doubleFree, ev)
			}
			frees++
		case goat.EventGCEnd:
			if *printFlag {
				fmt.Printf("[%d P %d] GC end\n", ev.Timestamp-minTicks, ev.P)
			}
			gcs++
		}
		if len(reuseWithoutFree)+len(doubleFree) > maxErrors {
			break
		}
	}
	spinner.Stop()

	if errcount := len(reuseWithoutFree) + len(doubleFree); errcount != 0 {
		tooMany := errcount > maxErrors
		if tooMany {
			errcount = maxErrors
			fmt.Fprintf(os.Stderr, "found >%d errors in trace:\n", maxErrors)
		} else {
			fmt.Fprintf(os.Stderr, "found %d errors in trace:\n", errcount)
		}
		for i := 0; i < errcount; i++ {
			ts1, ts2 := ^uint64(0), ^uint64(0)
			var e1, e2 *goat.Event
			if len(reuseWithoutFree) != 0 {
				ts1 = reuseWithoutFree[0].Timestamp
				e1 = &reuseWithoutFree[0]
			}
			if len(doubleFree) != 0 {
				ts2 = doubleFree[0].Timestamp
				e2 = &doubleFree[0]
			}
			if ts1 < ts2 {
				fmt.Fprintf(os.Stderr, "  allocated over slot 0x%x\n", e1.Address)
				reuseWithoutFree = reuseWithoutFree[1:]
			} else {
				fmt.Fprintf(os.Stderr, "  freed free slot 0x%x\n", e2.Address)
				doubleFree = doubleFree[1:]
			}
		}
		if tooMany {
			fmt.Fprintf(os.Stderr, "too many errors\n")
		}
	}
	fmt.Printf("Allocs: %d\n", allocs)
	fmt.Printf("Frees:  %d\n", frees)
	fmt.Printf("GCs:    %d\n", gcs)
}
