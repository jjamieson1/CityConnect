// Command c2stub runs a local stand-in for C2 (TrustIdentity) so CityConnect
// can be developed and tested without a live identity provider.
//
// With staff SSO as the only login path, nothing in CityConnect is demoable
// until something answers OIDC discovery. This is that something.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jjamieson1/CityConnect/devtools/c2stub"
)

func main() {
	addr := flag.String("addr", ":5173", "listen address")
	clientID := flag.String("client-id", "cityconnect-dev", "the one registered client id")
	clientSecret := flag.String("client-secret", "dev-secret", "client secret for HTTP Basic")
	issuer := flag.String("issuer", "", "public issuer; defaults to http://localhost<addr>/oidc")
	backchannel := flag.String("backchannel", "http://localhost:4021/api/c2/backchannel-logout",
		"CityConnect's back-channel logout endpoint")
	denyConsent := flag.Bool("deny-consent", false,
		"answer 403 on partner notifications, to exercise the consent-denied path")
	flag.Parse()

	stub, err := c2stub.New(c2stub.Options{
		ClientID:     *clientID,
		ClientSecret: *clientSecret,
		DenyConsent:  *denyConsent,
	})
	if err != nil {
		log.Fatalf("could not start stub: %v", err)
	}

	resolved := *issuer
	if resolved == "" {
		// --addr may be ":5173" or "127.0.0.1:5273"; only the bare-port form
		// needs a host prepended.
		host, port, err := net.SplitHostPort(*addr)
		if err != nil {
			log.Fatalf("could not parse --addr %q: %v", *addr, err)
		}
		if host == "" {
			host = "localhost"
		}
		resolved = "http://" + net.JoinHostPort(host, port) + "/oidc"
	}
	stub.SetIssuer(resolved)
	stub.SetBackchannelURL(*backchannel)

	fmt.Fprintf(os.Stderr, `
C2 stub listening on %s

  issuer                 %s
  discovery              %s/.well-known/openid-configuration
  client_id              %s
  back-channel logout    %s

Control endpoints:
  GET  /stub/login?sub=citizen-001         give a subject an active session (enables silent SSO)
  GET  /stub/logout[?sub=...]              sign out and fan out a back-channel logout
  GET  /stub/consent?sub=...&granted=false revoke consent, so notifications answer 403
  GET  /stub/notifications                 everything the partner endpoint accepted
  GET  /stub/callout?url=...&sub=...       drive a Service Card callout against CityConnect

Note: the token endpoint is /oidc/oauth/token, not /oidc/token — the same quirk
as the real deployment, so a client that hardcodes the obvious path fails here too.

`, *addr, resolved, resolved, *clientID, *backchannel)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           stub.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
