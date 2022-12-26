/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package tasks

import (
	gocontext "context"
	"errors"
	"fmt"

	"github.com/containerd/console"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/cmd/ctr/commands"
	gocni "github.com/containerd/go-cni"
	"github.com/containerd/typeurl"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

var startCommand = cli.Command{
	Name:      "start",
	Usage:     "start a container that has been created",
	ArgsUsage: "CONTAINER",
	Flags:     commands.TaskFlags,
	Action: func(context *cli.Context) error {
		id := context.Args().Get(0)
		if id == "" {
			return errors.New("container id must be provided")
		}

		client, ctx, cancel, err := commands.NewClient(context)
		if err != nil {
			return err
		}
		defer cancel()

		container, err := client.LoadContainer(ctx, id)
		if err != nil {
			return err
		}

		return StartTask(ctx, client, context, container)
	},
}

func StartTask(ctx gocontext.Context, client *containerd.Client, context *cli.Context, container containerd.Container) error {
	var detach = context.Bool("detach")

	exts, err := container.Extensions(ctx)
	if err != nil {
		return err
	}
	var network gocni.CNI
	if networkMeta, ok := exts[commands.CtrCniMetadataExtension]; ok {
		data, err := typeurl.UnmarshalAny(networkMeta)
		if err != nil {
			return fmt.Errorf("failed to unmarshal cni metadata extension  %s", commands.CtrCniMetadataExtension)
		}
		if data.(*commands.NetworkMetaData).EnableCni {
			if network, err = gocni.New(gocni.WithDefaultConf); err != nil {
				return err
			}
		}
	}

	spec, err := container.Spec(ctx)
	if err != nil {
		return err
	}
	var con console.Console
	if spec.Process.Terminal {
		con = console.Current()
		defer con.Reset()
		if err := con.SetRaw(); err != nil {
			return err
		}
	}

	opts := getNewTaskOpts(context)
	ioOpts := []cio.Opt{cio.WithFIFODir(context.String("fifo-dir"))}
	task, err := NewTask(ctx, client, container, context.String("checkpoint"), con, context.Bool("null-io"), context.String("log-uri"), ioOpts, opts...)
	if err != nil {
		return err
	}

	var statusC <-chan containerd.ExitStatus
	if !detach {
		defer func() {
			if network != nil {
				if err := network.Remove(ctx, commands.FullID(ctx, container), ""); err != nil {
					logrus.WithError(err).Error("network review")
				}
			}
			task.Delete(ctx)
		}()

		if statusC, err = task.Wait(ctx); err != nil {
			return err
		}
	}
	if context.IsSet("pid-file") {
		if err := commands.WritePidFile(context.String("pid-file"), int(task.Pid())); err != nil {
			return err
		}
	}
	if network != nil {
		netNsPath, err := getNetNSPath(ctx, task)
		if err != nil {
			return err
		}

		if _, err := network.Setup(ctx, commands.FullID(ctx, container), netNsPath); err != nil {
			return err
		}
	}

	if err := task.Start(ctx); err != nil {
		return err
	}
	if detach {
		return nil
	}

	if con != nil {
		if err := tasks.HandleConsoleResize(ctx, task, con); err != nil {
			logrus.WithError(err).Error("console resize")
		}
	} else {
		sigc := commands.ForwardAllSignals(ctx, task)
		defer commands.StopCatch(sigc)
	}

	status := <-statusC
	code, _, err := status.Result()
	if err != nil {
		return err
	}
	if _, err := task.Delete(ctx); err != nil {
		return err
	}
	if code != 0 {
		return cli.NewExitError("", int(code))
	}
	return nil
}
