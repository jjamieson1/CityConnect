// Command ccadm is the operator CLI.
//
// It exists mainly for the situations the console cannot reach. With C2 SSO as
// the only staff login there is no password reset and no local account: if the
// last administrator is locked out, or C2 is unavailable, this is the recovery
// path. It runs on the server, against the same database and configuration as
// the API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/agents"
	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/c2/oidc"
	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/requests"
	"github.com/jjamieson1/CityConnect/internal/seed"
	"github.com/jjamieson1/CityConnect/internal/store"
)

const usage = `ccadm — CityConnect operator CLI

Usage:
  ccadm <command> [flags]

Commands:
  migrate                Apply the schema (AutoMigrate plus full-text indexes)
  seed                   Install or verify the baseline configuration
  demo                   Install sample data for a local walkthrough
  grant-role             Grant a role to a C2 subject, creating the user if needed
  invite                 Invite by email; binds to their C2 identity on first sign-in
  list-users             List staff users
  issue-token            Mint a cc_pat_ personal access token
  revoke-token           Revoke a token by id
  verify-audit           Replay and verify the audit hash chain
  check-c2               Resolve C2 discovery and report the endpoints in use
  unlock                 Reactivate a user and clear their sessions
  reissue-references     Replace sequential request references with drawn ones

Run 'ccadm <command> -h' for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}

	if err := dispatch(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func dispatch(command string, args []string) error {
	switch command {
	case "migrate":
		return withDB(func(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
			if err := store.Migrate(db, log); err != nil {
				return err
			}
			fmt.Println("schema applied")
			return nil
		})

	case "seed":
		return withDB(func(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
			return seed.Run(ctx, db, cfg, log)
		})

	case "demo":
		return withDB(func(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
			if err := seed.Run(ctx, db, cfg, log); err != nil {
				return err
			}
			return seed.DemoData(ctx, db, log, cfg.ReferencePrefix)
		})

	case "grant-role":
		return grantRole(args)
	case "invite":
		return invite(args)
	case "list-users":
		return listUsers(args)
	case "issue-token":
		return issueToken(args)
	case "revoke-token":
		return revokeToken(args)
	case "verify-audit":
		return verifyAudit()
	case "check-c2":
		return checkC2()
	case "unlock":
		return unlock(args)
	case "reissue-references":
		return reissueReferences(args)

	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q; run 'ccadm help'", command)
}

func withDB(fn func(context.Context, *gorm.DB, *config.Config, *slog.Logger) error) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	db, err := store.Open(cfg.DB, log)
	if err != nil {
		return err
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return fn(ctx, db, cfg, log)
}

// grantRole is the day-one bootstrap and the break-glass recovery in one.
func grantRole(args []string) error {
	fs := flag.NewFlagSet("grant-role", flag.ExitOnError)
	sub := fs.String("sub", "", "C2 subject identifier (required)")
	email := fs.String("email", "", "email address, used when creating a new user")
	name := fs.String("name", "", "display name")
	role := fs.String("role", "admin", "role: readonly | agent | supervisor | admin")
	dept := fs.String("department", "", "department code, e.g. PW")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *sub == "" {
		fs.Usage()
		return errors.New("-sub is required")
	}
	if !domain.Role(*role).Valid() {
		return fmt.Errorf("unknown role %q", *role)
	}

	return withDB(func(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
		departmentID := ""
		if *dept != "" {
			var d domain.Department
			if err := db.WithContext(ctx).First(&d, "code = ?", strings.ToUpper(*dept)).Error; err != nil {
				return fmt.Errorf("unknown department %q", *dept)
			}
			departmentID = d.ID
		}

		var user domain.User
		err := db.WithContext(ctx).Where("c2_sub = ?", *sub).First(&user).Error

		switch {
		case err == nil:
			updates := map[string]any{"role": *role, "status": domain.UserActive}
			if departmentID != "" {
				updates["department_id"] = departmentID
			}
			if *name != "" {
				updates["name"] = *name
			}
			if err := db.WithContext(ctx).Model(&user).Updates(updates).Error; err != nil {
				return err
			}
			fmt.Printf("updated %s (%s) to role %s\n", user.Email, *sub, *role)

		case errors.Is(err, gorm.ErrRecordNotFound):
			addr := *email
			if addr == "" {
				addr = fmt.Sprintf("admin+%s@cityconnect.local", shortSub(*sub))
			}
			displayName := *name
			if displayName == "" {
				displayName = "Administrator"
			}
			user = domain.User{
				C2Sub: *sub, Email: strings.ToLower(addr), Name: displayName,
				Status: domain.UserActive, Role: domain.Role(*role),
				DepartmentID: departmentID, CrossDepartment: *role == string(domain.RoleAdmin),
			}
			if err := db.WithContext(ctx).Create(&user).Error; err != nil {
				return err
			}
			fmt.Printf("created %s (%s) with role %s\n", user.Email, *sub, *role)

		default:
			return err
		}

		audit.NewService(db, log).Record(ctx, audit.Actor{
			Type: audit.ActorJob, Label: "ccadm",
		}, audit.Entry{
			Action: "user.role_granted_cli", TargetType: "user", TargetID: user.ID,
			Summary: fmt.Sprintf("ccadm granted %s to %s", *role, *sub),
		})
		return nil
	})
}

// invite creates a staff record keyed on an email address, which binds to a C2
// identity on first sign-in.
//
// This is the counterpart to grant-role for the common case where the subject
// identifier is not yet known — nobody has one before they have signed in, and
// sign-in is what is being unblocked.
func invite(args []string) error {
	fs := flag.NewFlagSet("invite", flag.ExitOnError)
	email := fs.String("email", "", "email address on their C2 account (required)")
	name := fs.String("name", "", "display name")
	role := fs.String("role", "agent", "role: readonly | agent | supervisor | admin")
	dept := fs.String("department", "", "department code, e.g. PW")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		fs.Usage()
		return errors.New("-email is required")
	}
	if !domain.Role(*role).Valid() {
		return fmt.Errorf("unknown role %q", *role)
	}

	return withDB(func(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
		departmentID := ""
		if *dept != "" {
			var d domain.Department
			if err := db.WithContext(ctx).First(&d, "code = ?", strings.ToUpper(*dept)).Error; err != nil {
				return fmt.Errorf("unknown department %q", *dept)
			}
			departmentID = d.ID
		}

		svc := agents.NewService(db, cfg, oidc.New(cfg.C2), audit.NewService(db, log), log)
		user, err := svc.InviteUser(ctx, audit.Actor{Type: audit.ActorJob, Label: "ccadm"},
			agents.InviteInput{
				Email: *email, Name: *name, Role: domain.Role(*role),
				DepartmentID:    departmentID,
				CrossDepartment: *role == string(domain.RoleAdmin),
			})
		if err != nil {
			return err
		}

		fmt.Printf("invited %s as %s\n", user.Email, user.Role)
		fmt.Println("They can sign in with C2 SSO now; the account binds to their")
		fmt.Println("C2 identity the first time they do.")
		return nil
	})
}

func listUsers(args []string) error {
	fs := flag.NewFlagSet("list-users", flag.ExitOnError)
	role := fs.String("role", "", "filter by role")
	if err := fs.Parse(args); err != nil {
		return err
	}

	return withDB(func(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
		q := db.WithContext(ctx).Model(&domain.User{}).Preload("Department")
		if *role != "" {
			q = q.Where("role = ?", *role)
		}
		var users []domain.User
		if err := q.Order("role DESC, email ASC").Find(&users).Error; err != nil {
			return err
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "EMAIL\tNAME\tROLE\tSTATUS\tDEPARTMENT\tC2 SUBJECT\tLAST LOGIN")
		for _, u := range users {
			deptName := ""
			if u.Department != nil {
				deptName = u.Department.Code
			}
			last := "never"
			if u.LastLoginAt != nil {
				last = u.LastLoginAt.Format("2006-01-02 15:04")
			}
			sub := u.C2Sub
			if sub == "" {
				sub = "(not yet bound)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				u.Email, u.Name, u.Role, u.Status, deptName, sub, last)
		}
		return tw.Flush()
	})
}

func issueToken(args []string) error {
	fs := flag.NewFlagSet("issue-token", flag.ExitOnError)
	name := fs.String("name", "", "token name (required)")
	systemCode := fs.String("system", "", "connected system code")
	ownerEmail := fs.String("owner", "", "owning user's email")
	scopes := fs.String("scopes", "requests:read", "comma-separated scopes, or '*'")
	readOnly := fs.Bool("read-only", false, "issue a read-only token")
	expires := fs.Duration("expires-in", 0, "lifetime, e.g. 8760h; zero means no expiry")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		fs.Usage()
		return errors.New("-name is required")
	}

	return withDB(func(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
		svc := agents.NewService(db, cfg, oidc.New(cfg.C2), audit.NewService(db, log), log)

		var ownerID, systemID string
		if *ownerEmail != "" {
			var u domain.User
			if err := db.WithContext(ctx).First(&u, "LOWER(email) = ?", strings.ToLower(*ownerEmail)).Error; err != nil {
				return fmt.Errorf("unknown user %q", *ownerEmail)
			}
			ownerID = u.ID
		}
		if *systemCode != "" {
			var sys domain.ConnectedSystem
			if err := db.WithContext(ctx).First(&sys, "code = ?", strings.ToUpper(*systemCode)).Error; err != nil {
				return fmt.Errorf("unknown connected system %q", *systemCode)
			}
			systemID = sys.ID
		}

		issued, err := svc.IssueToken(ctx, audit.Actor{Type: audit.ActorJob, Label: "ccadm"},
			agents.IssueTokenInput{
				Name: *name, OwnerID: ownerID, SystemID: systemID,
				Scopes: strings.Split(*scopes, ","), ReadOnly: *readOnly, ExpiresIn: *expires,
			})
		if err != nil {
			return err
		}

		// Printed once, to stdout only, so it can be piped somewhere safe
		// without the surrounding chatter.
		fmt.Fprintln(os.Stderr, "Token issued. It is shown once and only its hash is stored:")
		fmt.Println(issued.Token)
		return nil
	})
}

func revokeToken(args []string) error {
	fs := flag.NewFlagSet("revoke-token", flag.ExitOnError)
	id := fs.String("id", "", "token id (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("-id is required")
	}

	return withDB(func(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
		svc := agents.NewService(db, cfg, oidc.New(cfg.C2), audit.NewService(db, log), log)
		if err := svc.RevokeToken(ctx, audit.Actor{Type: audit.ActorJob, Label: "ccadm"}, *id); err != nil {
			return err
		}
		fmt.Println("token revoked")
		return nil
	})
}

func verifyAudit() error {
	return withDB(func(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
		res, err := audit.NewService(db, log).Verify(ctx)
		if err != nil {
			return err
		}
		if res.Valid {
			fmt.Printf("audit chain intact: %d entries verified\n", res.Checked)
			return nil
		}
		return fmt.Errorf("audit chain broken at seq %d (entry %s): %s",
			res.BrokenAt, res.BrokenID, res.BrokenWhy)
	})
}

// checkC2 prints the endpoints actually in use, which is the fastest way to
// settle the "why am I getting 401s" question.
func checkC2() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	provider := oidc.New(cfg.C2)
	fmt.Printf("configured issuer:   %s\n", cfg.C2.Issuer)
	fmt.Printf("portal origin:       %s\n", cfg.C2.PortalOrigin)
	fmt.Printf("client id:           %s\n", cfg.C2.ClientID)
	fmt.Printf("redirect uri:        %s\n", cfg.C2.RedirectURL)
	fmt.Printf("discovery url:       %s\n\n", cfg.C2.DiscoveryURL())

	doc, err := provider.Discovery(ctx)
	if err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}

	fmt.Printf("discovered issuer:   %s\n", doc.Issuer)
	fmt.Printf("authorization:       %s\n", doc.AuthorizationEndpoint)
	fmt.Printf("token:               %s\n", doc.TokenEndpoint)
	fmt.Printf("userinfo:            %s\n", doc.UserInfoEndpoint)
	fmt.Printf("jwks:                %s\n", doc.JWKSURI)
	fmt.Printf("end session:         %s\n", doc.EndSessionEndpoint)
	fmt.Printf("pkce methods:        %v\n", doc.CodeChallengeMethods)

	if err := provider.Check(ctx); err != nil {
		return fmt.Errorf("JWKS check failed: %w", err)
	}
	fmt.Println("\nJWKS fetched and cached. C2 is reachable and correctly configured.")
	return nil
}

// reissueReferences replaces guessable, counter-era references with drawn ones.
//
// References used to be allocated in sequence (SR-2026-000001), which makes the
// city's whole caseload enumerable the moment a reference is quotable on a
// public tracking endpoint. New requests are drawn at random; this converts what
// is already in the table.
//
// It rewrites the identifier a resident may already be holding, so it reports by
// default and only writes when told to. Every rewrite lands in the audit chain.
func reissueReferences(args []string) error {
	fs := flag.NewFlagSet("reissue-references", flag.ExitOnError)
	confirm := fs.Bool("confirm", false, "actually rewrite; without it this only reports")
	prefix := fs.String("prefix", "", "reference prefix to issue under (default: CC_REFERENCE_PREFIX)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	return withDB(func(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
		use := requests.NormalizeReferencePrefix(firstNonEmpty(*prefix, cfg.ReferencePrefix))

		var all []domain.Request
		if err := db.WithContext(ctx).Select("id", "reference").Find(&all).Error; err != nil {
			return err
		}

		stale := make([]domain.Request, 0, len(all))
		for _, r := range all {
			if !requests.IsGeneratedReference(r.Reference) {
				stale = append(stale, r)
			}
		}

		fmt.Printf("%d request(s); %d carry a sequential reference\n", len(all), len(stale))
		if len(stale) == 0 {
			return nil
		}
		if !*confirm {
			for i, r := range stale {
				if i == 10 {
					fmt.Printf("  … and %d more\n", len(stale)-i)
					break
				}
				fmt.Printf("  %s\n", r.Reference)
			}
			fmt.Println("\nNothing written. Re-run with -confirm to reissue these under " + use + "-.")
			return nil
		}

		aud := audit.NewService(db, log)
		actor := audit.JobActor("ccadm reissue-references")

		var done int
		for _, r := range stale {
			// The unique index is the arbiter of a collision, exactly as it is
			// for a live submission, so retry the draw rather than failing the run.
			var lastErr error
			for attempt := 0; attempt < 3; attempt++ {
				next, err := requests.NewReference(use)
				if err != nil {
					return err
				}
				err = db.WithContext(ctx).Model(&domain.Request{}).
					Where("id = ?", r.ID).Update("reference", next).Error
				if err != nil {
					lastErr = err
					continue
				}
				aud.Record(ctx, actor, audit.Entry{
					Action: "request.reference_reissued", TargetType: "request", TargetID: r.ID,
					Summary: r.Reference + " -> " + next,
				})
				lastErr = nil
				done++
				break
			}
			if lastErr != nil {
				return fmt.Errorf("reissue %s: %w", r.Reference, lastErr)
			}
		}

		fmt.Printf("%d reference(s) reissued under %s-.\n", done, use)
		fmt.Println("Anything printed or emailed with an old reference no longer resolves.")
		return nil
	})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// unlock reactivates a user and clears their sessions, for the case where
// somebody was suspended in error and cannot get back in.
func unlock(args []string) error {
	fs := flag.NewFlagSet("unlock", flag.ExitOnError)
	email := fs.String("email", "", "user's email address (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("-email is required")
	}

	return withDB(func(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
		var u domain.User
		if err := db.WithContext(ctx).First(&u, "LOWER(email) = ?", strings.ToLower(*email)).Error; err != nil {
			return fmt.Errorf("unknown user %q", *email)
		}
		if err := db.WithContext(ctx).Model(&u).Update("status", domain.UserActive).Error; err != nil {
			return err
		}
		res := db.WithContext(ctx).Model(&domain.Session{}).
			Where("user_id = ? AND revoked_at IS NULL", u.ID).
			Updates(map[string]any{"revoked_at": time.Now().UTC(), "revoke_reason": "ccadm_unlock"})

		fmt.Printf("%s reactivated; %d stale session(s) cleared. They can sign in again with C2 SSO.\n",
			u.Email, res.RowsAffected)
		return nil
	})
}

func shortSub(sub string) string {
	if len(sub) <= 8 {
		return sub
	}
	return sub[:8]
}
