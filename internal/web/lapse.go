package web

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/hbroek/weft/internal/store"
)

// The daily check of the domains that accounts depend on, and what is done about one that stopped working. The rule
// is in store/lapse.go: three failed checks in a row start a grace period; the account ends if it runs out.

// checkBatch is how many domains one pass looks at, so that a thousand of them are spread over the minutes of a day
// and no pass is long.
const checkBatch = 25

// worksFor reports whether the account's domain verifies: its TXT record is there and it reaches this server.
func (s *Server) worksFor(ctx context.Context, d *store.Domain) bool {
	return s.owns(ctx, d) && s.reaches(ctx, d, "")
}

// checkDomains checks the bound domains that are due, records what it finds, and sends the warnings that are
// due. Only one runs at a time.
func (s *Server) checkDomains(ctx context.Context, now time.Time) {
	if s.cfg.Host != "" { // the operator's own address: nothing to prove, and nobody to take it away
		return
	}
	if !s.checking.CompareAndSwap(false, true) {
		return
	}
	defer s.checking.Store(false)
	due, err := s.st.DomainsDue(now, checkBatch)
	if err != nil {
		log.Printf("domain checks: %v", err)
		return
	}
	ok := make([]bool, len(due))
	failed := 0
	for i, d := range due {
		if ctx.Err() != nil {
			return
		}
		if ok[i] = s.worksFor(ctx, d); !ok[i] {
			failed++
		}
	}
	if len(due) >= 5 && failed == len(due) {
		// Every one failing at once says more about this server's DNS or network than about the domains.
		log.Printf("domain checks: all %d failed together; not counting them, trying again in an hour", len(due))
		for _, d := range due {
			s.st.Postpone(d.Name, now, time.Hour)
		}
		return
	}
	for i, d := range due {
		l, newly, err := s.st.RecordCheck(d.Name, ok[i], now)
		if err != nil {
			log.Printf("domain check of %s: %v", d.Name, err)
			continue
		}
		if newly {
			log.Printf("the domain %s of %s has stopped verifying: the account ends on %s unless it verifies again", d.Domain, d.Name, l.Deadline.Format("2006-01-02"))
		}
	}
	s.checkHandles(ctx, now)
	s.warnByMail(ctx, now)
}

// checkHandles looks at the active custom handles of accounts that are not tied to them. A handle that stops
// reaching this server for three checks in a row stops being the account's address, which goes back to the default
// one; nobody is removed (see store.ReachDue).
func (s *Server) checkHandles(ctx context.Context, now time.Time) {
	due, err := s.st.ReachDue(now, checkBatch)
	if err != nil || len(due) == 0 {
		return
	}
	ok := make([]bool, len(due))
	failed := 0
	for i, d := range due {
		if ctx.Err() != nil {
			return
		}
		if ok[i] = s.reaches(ctx, d, ""); !ok[i] {
			failed++
		}
	}
	if len(due) >= 5 && failed == len(due) {
		log.Printf("handle checks: all %d failed together; not counting them, trying again in an hour", len(due))
		for _, d := range due {
			s.st.Postpone(d.Name, now, time.Hour)
		}
		return
	}
	for i, d := range due {
		off, err := s.st.RecordReach(d.Name, ok[i], now)
		if err != nil {
			log.Printf("handle check of %s: %v", d.Name, err)
		} else if off {
			log.Printf("the handle %s of %s has not reached this server for %d days: %s is served at its default address again", d.Domain, d.Name, store.FailuresToLapse, d.Name)
		}
	}
}

// warnByMail sends the warning to the owner of each lapsed domain, and a reminder before the end, where there is a
// connector and the account has an address. What was sent is remembered, so each goes once; with no connector
// nothing is marked, and a connector added later sends them at its first pass.
func (s *Server) warnByMail(ctx context.Context, now time.Time) {
	if s.cfg.Mail == nil {
		return
	}
	list, err := s.st.Lapsing()
	if err != nil {
		return
	}
	for _, l := range list {
		level := 0
		switch {
		case !l.Lapsed || l.Admin:
			continue
		case l.Mailed < 2 && l.Deadline.Sub(now) <= store.ReminderDays*24*time.Hour:
			level = 2
		case l.Mailed < 1:
			level = 1
		default:
			continue
		}
		to := s.st.EmailOf(l.Name)
		if to == "" {
			continue
		}
		subject, body := s.lapseMail(l, level)
		if err := s.cfg.Mail(ctx, to, subject, body); err != nil {
			log.Printf("mailing %s about the domain: %v", l.Name, err)
			continue
		}
		s.st.MarkMailed(l.Name, level)
	}
}

func (s *Server) lapseMail(l store.Lapse, level int) (subject, body string) {
	end := l.Deadline.Format("2 January 2006")
	profile := fmt.Sprintf("%s://%s.%s/profile", s.cfg.Scheme, l.Name, s.cfg.BaseDomain)
	if level == 2 {
		subject = "Your Weft account ends on " + end
	} else {
		subject = "Your domain " + l.Domain + " no longer works with Weft"
	}
	body = fmt.Sprintf(`Your Weft account %s is tied to the domain %s. For %d days in a row the domain has not verified: its TXT record is missing, or it no longer reaches the server.

If it does not verify again by %s, the account is removed. It then waits %d days in the trash, so your posts can still be downloaded until then.

To fix it, put the DNS records back and press "Check now" on your profile page: %s
To leave without losing anything, move your account to another server, or download your data from the same page.
`, l.Name, l.Domain, store.FailuresToLapse, end, store.TrashDays, profile)
	return
}

// lapseWarning is what the owner sees on every page while the account's domain has lapsed, or "".
func (s *Server) lapseWarning(name string) string {
	l := s.st.LapseOf(name)
	if l == nil || !l.Lapsed {
		return ""
	}
	return fmt.Sprintf("Your domain %s has not verified since %s. If it does not work again by %s, this account is removed.",
		l.Domain, l.Since.Format("2 January"), l.Deadline.Format("2 January 2006"))
}
