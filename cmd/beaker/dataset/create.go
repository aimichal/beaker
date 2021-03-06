package dataset

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/beaker/fileheap/cli"
	"github.com/fatih/color"
	"github.com/pkg/errors"
	kingpin "gopkg.in/alecthomas/kingpin.v2"

	"github.com/allenai/beaker/api"
	beaker "github.com/allenai/beaker/client"
	"github.com/allenai/beaker/config"
)

type createOptions struct {
	description string
	name        string
	quiet       bool
	source      string
	org         string
	fileheap    bool
}

func newCreateCmd(
	parent *kingpin.CmdClause,
	parentOpts *datasetOptions,
	config *config.Config,
) {
	o := &createOptions{}
	cmd := parent.Command("create", "Create a new dataset")
	cmd.Action(func(c *kingpin.ParseContext) error {
		beaker, err := beaker.NewClient(parentOpts.addr, config.UserToken)
		if err != nil {
			return err
		}
		if o.org == "" {
			o.org = config.DefaultOrg
		}
		return o.run(beaker)
	})

	cmd.Flag("desc", "Assign a description to the dataset").StringVar(&o.description)
	cmd.Flag("name", "Assign a name to the dataset").Short('n').StringVar(&o.name)
	cmd.Flag("quiet", "Only display created dataset's ID").Short('q').BoolVar(&o.quiet)
	cmd.Flag("org", "Org that will own the created experiment").Short('o').StringVar(&o.org)
	cmd.Flag("fileheap", "Store the dataset in FileHeap").Default("true").BoolVar(&o.fileheap)
	cmd.Arg("source", "Path to a file or directory containing the data").
		Required().ExistingFileOrDirVar(&o.source)
}

func (o *createOptions) run(beaker *beaker.Client) error {
	ctx := context.TODO()

	info, err := os.Stat(o.source)
	if err != nil {
		return err
	}
	if info.Mode()&(os.ModeSymlink|os.ModeNamedPipe|os.ModeSocket|os.ModeDevice) != 0 {
		return errors.Errorf("%s is a %s", o.source, modeToString(info.Mode()))
	}

	spec := api.DatasetSpec{
		Description:  o.description,
		Organization: o.org,
		FileHeap:     o.fileheap,
	}
	if !info.IsDir() {
		// If uploading a single file, treat it as a single-file dataset.
		spec.Filename = info.Name()
	}

	dataset, err := beaker.CreateDataset(ctx, spec, o.name)
	if err != nil {
		return err
	}

	if !o.quiet {
		if o.name == "" {
			fmt.Printf("Uploading %s to %s\n", color.GreenString(o.source), color.CyanString(dataset.ID()))
		} else {
			fmt.Printf("Uploading %s to %s (%s)\n", color.GreenString(o.source), color.CyanString(o.name), dataset.ID())
		}
	}

	if info.IsDir() {
		if o.fileheap {
			var tracker cli.ProgressTracker = cli.NoTracker
			if !o.quiet {
				files, bytes, err := cli.UploadStats(o.source)
				if err != nil {
					return err
				}
				tracker = cli.BoundedTracker(ctx, files, bytes)
			}
			err = cli.Upload(ctx, o.source, dataset.Storage, "", tracker, 32)
		} else {
			err = uploadDirectory(ctx, dataset, o.source, !o.quiet)
		}
	} else {
		err = uploadFile(ctx, dataset.FileRef(info.Name()), o.source, info.Size())
	}
	if err != nil {
		return err
	}

	if err := dataset.Commit(ctx); err != nil {
		return errors.WithMessage(err, "failed to commit dataset")
	}

	if o.quiet {
		fmt.Println(dataset.ID())
	} else if !(info.IsDir() && o.fileheap) {
		fmt.Println("Done.")
	}
	return nil
}

func modeToString(mode os.FileMode) string {
	switch {
	case mode&os.ModeDir != 0:
		return "directory"
	case mode&os.ModeSymlink != 0:
		return "symbolic link"
	case mode&os.ModeNamedPipe != 0:
		return "named pipe"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeDevice != 0:
		return "device"
	default:
		return "file"
	}
}

func uploadDirectory(
	ctx context.Context,
	dataset *beaker.DatasetHandle,
	directory string,
	showWarnings bool,
) error {
	visitor := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return errors.WithStack(err)
		}

		if info.IsDir() {
			return nil
		}

		relpath, err := filepath.Rel(directory, path)
		if err != nil {
			return errors.WithStack(err)
		}

		if !info.Mode().IsRegular() {
			if showWarnings {
				fmt.Printf("%s: Skipping %s: %s\n", color.YellowString("Warning"), modeToString(info.Mode()), relpath)
			}
			return nil
		}

		return uploadFile(ctx, dataset.FileRef(relpath), path, info.Size())
	}

	return filepath.Walk(directory, visitor)
}

func uploadFile(ctx context.Context, fileRef *beaker.FileHandle, source string, length int64) error {
	file, err := os.Open(source)
	if err != nil {
		return errors.WithStack(err)
	}
	defer file.Close()

	return fileRef.Upload(ctx, file, length)
}
