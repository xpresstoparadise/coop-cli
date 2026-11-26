package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/xpresstoparadise/coop-cli/internal/server"
)

func dbFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("COOP_DB")
	if def == "" {
		def = "coop.db"
	}
	return fs.String("db", def, "SQLite database ($COOP_DB)")
}

func cmdServe(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("coop serve", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	db := dbFlag(fs)
	addr := fs.String("addr", "127.0.0.1:8470", "listen address")
	base := fs.String("base-url", os.Getenv("COOP_BASE_URL"), "public URL used in invitations, e.g. https://coop.example.org ($COOP_BASE_URL)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *base == "" {
		*base = "http://" + *addr
	}
	srv, err := server.Open(*db, *base)
	if err != nil {
		return err
	}
	defer srv.Close()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.RunPurge(ctx, time.Hour)
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	log.Printf("coop %s serving on %s (public %s, db %s)", Version, ln.Addr(), *base, *db)
	if err := hs.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func cmdKey(ctx context.Context, env *Env, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: coop key add --name NAME | coop key list | coop key revoke ID")
	}
	fs := flag.NewFlagSet("coop key "+args[0], flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	db := dbFlag(fs)
	name := fs.String("name", "", "who the key is for")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	srv, err := server.Open(*db, "")
	if err != nil {
		return err
	}
	defer srv.Close()
	switch args[0] {
	case "add":
		if *name == "" {
			return errors.New("key add needs --name")
		}
		id, key, err := srv.AddKey(*name)
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "host key %d for %q (shown once):\n%s\n", id, *name, key)
	case "list":
		tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tCREATED\tREVOKED")
		keys, err := srv.Keys()
		if err != nil {
			return err
		}
		for _, k := range keys {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", k.ID, k.Name, k.CreatedAt, k.RevokedAt)
		}
		return tw.Flush()
	case "revoke":
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil {
			return errors.New("usage: coop key revoke ID")
		}
		if err := srv.RevokeKey(id); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "revoked host key %d\n", id)
	default:
		return fmt.Errorf("unknown key command %q", args[0])
	}
	return nil
}

func cmdPurge(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("coop purge", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	db := dbFlag(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	srv, err := server.Open(*db, "")
	if err != nil {
		return err
	}
	defer srv.Close()
	n, err := srv.Purge()
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "deleted %d rooms\n", n)
	return nil
}
