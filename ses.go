package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/logincreds"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/aws-sdk-go-v2/service/signin"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// SES is the AWS side of a project: SESv2 and STS clients for one profile and region.
type SES struct {
	cfg Config
	ses *sesv2.Client
	sts *sts.Client
}

// newSES builds the clients with credentials from the profile's `aws login`
// session and nothing else: environment keys, static profile keys and
// instance roles are never used, so mail only goes out as whoever logged in.
func newSES(ctx context.Context, cfg Config) (*SES, error) {
	ac, err := loginConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &SES{cfg: cfg, ses: sesv2.NewFromConfig(ac), sts: sts.NewFromConfig(ac)}, nil
}

var errNotLoggedIn = errors.New("not logged in")

// needsLogin reports whether err means there is no usable login session.
func needsLogin(err error) bool {
	return errors.Is(err, errNotLoggedIn) || err != nil && strings.Contains(err.Error(), "please reauthenticate")
}

// loginConfig loads the profile with credentials taken only from the login
// session `aws login` recorded in it, refreshed from the cached token.
func loginConfig(ctx context.Context, cfg Config) (aws.Config, error) {
	ac, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(cfg.Region),
		config.WithSharedConfigProfile(cfg.profile()),
		config.WithCredentialsProvider(aws.AnonymousCredentials{}),
		// SES allows one request per second outside of sending; adaptive
		// retries slow down instead of failing when a list is paged quickly.
		config.WithRetryMode(aws.RetryModeAdaptive),
		config.WithRetryMaxAttempts(10),
	)
	session := ""
	for _, src := range ac.ConfigSources {
		if sc, ok := src.(config.SharedConfig); ok {
			session = sc.LoginSession
		}
	}
	var missing config.SharedConfigProfileNotExistError
	if errors.As(err, &missing) || err == nil && session == "" {
		return ac, fmt.Errorf("%w: profile %s has no aws login session", errNotLoggedIn, cfg.profile())
	}
	if err != nil {
		return ac, err
	}
	path, err := logincreds.StandardCachedTokenFilepath(session, os.Getenv("AWS_LOGIN_CACHE_DIRECTORY"))
	if err != nil {
		return ac, err
	}
	ac.Credentials = aws.NewCredentialsCache(logincreds.New(signin.NewFromConfig(ac), path))
	return ac, nil
}

// Account is the sending state of the SES account in this region.
type Account struct {
	ID, Arn     string
	Production  bool
	Sent, Quota float64 // last 24 hours; Quota -1 means unlimited
	Rate        float64 // maximum recipients per second
	Status      string  // HEALTHY, PROBATION or SHUTDOWN
	Tracking    bool    // opens and clicks are counted (VDM engagement tracking)
}

func (s *SES) account(ctx context.Context) (Account, error) {
	id, err := s.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Account{}, err
	}
	out, err := s.ses.GetAccount(ctx, &sesv2.GetAccountInput{})
	if err != nil {
		return Account{}, err
	}
	a := Account{
		ID: aws.ToString(id.Account), Arn: aws.ToString(id.Arn),
		Production: out.ProductionAccessEnabled, Status: aws.ToString(out.EnforcementStatus),
	}
	if q := out.SendQuota; q != nil {
		a.Sent, a.Quota, a.Rate = q.SentLast24Hours, q.Max24HourSend, q.MaxSendRate
	}
	if v := out.VdmAttributes; v != nil && v.DashboardAttributes != nil {
		a.Tracking = v.VdmEnabled == types.FeatureStatusEnabled && v.DashboardAttributes.EngagementMetrics == types.FeatureStatusEnabled
	}
	return a, nil
}

// enableTracking turns on Virtual Deliverability Manager with engagement
// tracking for the account, which SES bills per email sent.
func (s *SES) enableTracking(ctx context.Context) error {
	_, err := s.ses.PutAccountVdmAttributes(ctx, &sesv2.PutAccountVdmAttributesInput{
		VdmAttributes: &types.VdmAttributes{
			VdmEnabled:          types.FeatureStatusEnabled,
			DashboardAttributes: &types.DashboardAttributes{EngagementMetrics: types.FeatureStatusEnabled},
		},
	})
	return err
}

// configSet names the configuration set a campaign is sent through, which
// is what lets SES count its deliveries, opens and clicks separately.
func configSet(slug string) string {
	name := "goletter-" + slug
	return name[:min(len(name), 64)]
}

func (s *SES) ensureConfigSet(ctx context.Context, name string) error {
	_, err := s.ses.CreateConfigurationSet(ctx, &sesv2.CreateConfigurationSetInput{ConfigurationSetName: aws.String(name)})
	var exists *types.AlreadyExistsException
	if errors.As(err, &exists) {
		return nil
	}
	return err
}

// Stats are the SES counts for one campaign.
type Stats struct {
	Sent, Delivered, Opened, Clicked, Bounced, Complaints int64
	FetchedAt                                             time.Time
}

// stats sums the campaign's metrics from the day it was created. SES keeps
// 60 days of them, so the window is capped there.
func (s *SES) stats(ctx context.Context, slug string, since time.Time) (Stats, error) {
	now := time.Now().UTC()
	start := since.UTC().Truncate(24 * time.Hour)
	if floor := now.AddDate(0, 0, -60); start.Before(floor) {
		start = floor
	}
	metrics := []types.Metric{types.MetricSend, types.MetricDelivery, types.MetricOpen, types.MetricClick,
		types.MetricPermanentBounce, types.MetricTransientBounce, types.MetricComplaint}
	var queries []types.BatchGetMetricDataQuery
	for _, m := range metrics {
		queries = append(queries, types.BatchGetMetricDataQuery{
			Id:         aws.String(string(m)),
			Namespace:  types.MetricNamespaceVdm,
			Metric:     m,
			Dimensions: map[string]string{string(types.MetricDimensionNameConfigurationSet): configSet(slug)},
			StartDate:  aws.Time(start),
			EndDate:    aws.Time(now),
		})
	}
	out, err := s.ses.BatchGetMetricData(ctx, &sesv2.BatchGetMetricDataInput{Queries: queries})
	if err != nil {
		return Stats{}, err
	}
	if len(out.Errors) > 0 {
		return Stats{}, errors.New(aws.ToString(out.Errors[0].Message))
	}
	st := Stats{FetchedAt: now}
	for _, r := range out.Results {
		var n int64
		for _, v := range r.Values {
			n += v
		}
		switch types.Metric(aws.ToString(r.Id)) {
		case types.MetricSend:
			st.Sent = n
		case types.MetricDelivery:
			st.Delivered = n
		case types.MetricOpen:
			st.Opened = n
		case types.MetricClick:
			st.Clicked = n
		case types.MetricPermanentBounce, types.MetricTransientBounce:
			st.Bounced += n
		case types.MetricComplaint:
			st.Complaints = n
		}
	}
	return st, nil
}

// identities lists the identities that are verified for sending.
func (s *SES) identities(ctx context.Context) ([]string, error) {
	out, err := s.ses.ListEmailIdentities(ctx, &sesv2.ListEmailIdentitiesInput{PageSize: aws.Int32(100)})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, id := range out.EmailIdentities {
		if id.SendingEnabled {
			names = append(names, aws.ToString(id.IdentityName))
		}
	}
	return names, nil
}

func (s *SES) lists(ctx context.Context) ([]string, error) {
	out, err := s.ses.ListContactLists(ctx, &sesv2.ListContactListsInput{})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range out.ContactLists {
		names = append(names, aws.ToString(l.ContactListName))
	}
	return names, nil
}

func (s *SES) topics(ctx context.Context, list string) ([]string, error) {
	out, err := s.ses.GetContactList(ctx, &sesv2.GetContactListInput{ContactListName: aws.String(list)})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, t := range out.Topics {
		names = append(names, aws.ToString(t.TopicName))
	}
	return names, nil
}

func (s *SES) createList(ctx context.Context, name string) error {
	_, err := s.ses.CreateContactList(ctx, &sesv2.CreateContactListInput{ContactListName: aws.String(name)})
	return err
}

// Contact is a member of the contact list.
type Contact struct {
	Email      string
	Subscribed bool // opted in to the configured topic, or not unsubscribed when no topic is set
}

// contacts lists every contact with its subscription state.
func (s *SES) contacts(ctx context.Context) ([]Contact, error) {
	cs := []Contact{}
	err := s.eachContact(ctx, nil, func(c types.Contact) {
		cs = append(cs, Contact{Email: aws.ToString(c.EmailAddress), Subscribed: s.subscribed(c)})
	})
	return cs, err
}

// subscribers lists the addresses SES considers opted in to the topic.
func (s *SES) subscribers(ctx context.Context) ([]string, error) {
	filter := &types.ListContactsFilter{FilteredStatus: types.SubscriptionStatusOptIn}
	if s.cfg.Topic != "" {
		filter.TopicFilter = &types.TopicFilter{TopicName: aws.String(s.cfg.Topic), UseDefaultIfPreferenceUnavailable: true}
	}
	var emails []string
	err := s.eachContact(ctx, filter, func(c types.Contact) { emails = append(emails, aws.ToString(c.EmailAddress)) })
	return emails, err
}

func (s *SES) eachContact(ctx context.Context, filter *types.ListContactsFilter, fn func(types.Contact)) error {
	pg := sesv2.NewListContactsPaginator(s.ses, &sesv2.ListContactsInput{
		ContactListName: aws.String(s.cfg.List), Filter: filter, PageSize: aws.Int32(100),
	})
	for pg.HasMorePages() {
		out, err := pg.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, c := range out.Contacts {
			fn(c)
		}
	}
	return nil
}

// deleteContact removes the address from the contact list altogether.
func (s *SES) deleteContact(ctx context.Context, email string) error {
	_, err := s.ses.DeleteContact(ctx, &sesv2.DeleteContactInput{ContactListName: aws.String(s.cfg.List), EmailAddress: aws.String(email)})
	return err
}

// setSubscribed opts a contact in to or out of the configured topic, or of
// the whole list when no topic is set, keeping their other preferences.
func (s *SES) setSubscribed(ctx context.Context, email string, on bool) error {
	c, err := s.ses.GetContact(ctx, &sesv2.GetContactInput{ContactListName: aws.String(s.cfg.List), EmailAddress: aws.String(email)})
	if err != nil {
		return err
	}
	in := &sesv2.UpdateContactInput{
		ContactListName:  aws.String(s.cfg.List),
		EmailAddress:     aws.String(email),
		AttributesData:   c.AttributesData,
		TopicPreferences: c.TopicPreferences,
		UnsubscribeAll:   c.UnsubscribeAll,
	}
	switch {
	case s.cfg.Topic == "":
		in.UnsubscribeAll = !on
	default:
		status := types.SubscriptionStatusOptOut
		if on {
			status, in.UnsubscribeAll = types.SubscriptionStatusOptIn, false
		}
		i := slices.IndexFunc(in.TopicPreferences, func(p types.TopicPreference) bool { return aws.ToString(p.TopicName) == s.cfg.Topic })
		if i < 0 {
			i = len(in.TopicPreferences)
			in.TopicPreferences = append(in.TopicPreferences, types.TopicPreference{TopicName: aws.String(s.cfg.Topic)})
		}
		in.TopicPreferences[i].SubscriptionStatus = status
	}
	_, err = s.ses.UpdateContact(ctx, in)
	return err
}

func (s *SES) subscribed(c types.Contact) bool {
	if c.UnsubscribeAll {
		return false
	}
	if s.cfg.Topic == "" {
		return true
	}
	for _, prefs := range [][]types.TopicPreference{c.TopicPreferences, c.TopicDefaultPreferences} {
		for _, p := range prefs {
			if aws.ToString(p.TopicName) == s.cfg.Topic {
				return p.SubscriptionStatus == types.SubscriptionStatusOptIn
			}
		}
	}
	return false
}

// unsubscribeURL is the placeholder SES replaces with the recipient's
// unsubscribe link when mail is sent with list management.
const unsubscribeURL = "{{amazonSESUnsubscribeUrl}}"

// template passes the rendered mail inline; SES fills the unsubscribe link.
func (s *SES) template(m Mail) *types.Template {
	c := &types.EmailTemplateContent{Subject: aws.String(m.Subject), Html: aws.String(m.HTML)}
	if m.Text != "" {
		c.Text = aws.String(m.Text)
	}
	return &types.Template{TemplateContent: c, TemplateData: aws.String("{}")}
}

func (s *SES) listOptions() *types.ListManagementOptions {
	o := &types.ListManagementOptions{ContactListName: aws.String(s.cfg.List)}
	if s.cfg.Topic != "" {
		o.TopicName = aws.String(s.cfg.Topic)
	}
	return o
}

func tags(slug string) []types.MessageTag {
	return []types.MessageTag{{Name: aws.String("campaign"), Value: aws.String(slug)}}
}

// send delivers m to one address and returns the SES message id. Campaign
// mail goes through the campaign's configuration set with list management,
// which fills the unsubscribe link but also adds unknown recipients to the
// contact list; test mail therefore goes without either.
func (s *SES) send(ctx context.Context, tag string, m Mail, to string, campaign bool) (string, error) {
	in := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(s.cfg.From),
		Destination:      &types.Destination{ToAddresses: []string{to}},
		Content:          &types.EmailContent{Template: s.template(m)},
		EmailTags:        tags(tag),
	}
	if campaign {
		in.ConfigurationSetName = aws.String(configSet(tag))
		in.ListManagementOptions = s.listOptions()
	}
	out, err := s.ses.SendEmail(ctx, in)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.MessageId), nil
}

// sendTest sends m to one address with the subject marked as a test and
// the unsubscribe link disarmed, since SES only fills it for list mail.
func (s *SES) sendTest(ctx context.Context, slug string, m Mail, to string) error {
	m.Subject = "[test] " + m.Subject
	m.HTML = strings.ReplaceAll(m.HTML, unsubscribeURL, "#")
	m.Text = strings.ReplaceAll(m.Text, unsubscribeURL, "#")
	_, err := s.send(ctx, slug+"-test", m, to, false)
	return err
}

// sendBatch sends m to every address concurrently through the campaign's
// configuration set and records each result.
// Per-recipient rejections are recorded and sending goes on; any other error
// (credentials, throttling, account state) is returned so the run can stop.
// SendBulkEmail would need fewer calls but has no list management, and with
// it no SES unsubscribe links.
func (s *SES) sendBatch(ctx context.Context, slug string, m Mail, to []string) ([]sendRecord, error) {
	recs := make([]sendRecord, len(to))
	errs := make([]error, len(to))
	var wg sync.WaitGroup
	for i, addr := range to {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := s.send(ctx, slug, m, addr, true)
			recs[i] = sendRecord{Email: addr, MessageID: id, Status: statusSuccess, Time: time.Now()}
			if err != nil {
				errs[i] = err
				recs[i].Status, recs[i].Error = "FAILED", err.Error()
				var ae smithy.APIError
				if errors.As(err, &ae) {
					recs[i].Status = ae.ErrorCode()
				}
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		var rejected *types.MessageRejected
		var request *types.BadRequestException
		if err != nil && !errors.As(err, &rejected) && !errors.As(err, &request) {
			return recs, err
		}
	}
	return recs, nil
}

// loginCommand returns the AWS CLI command that logs the project's profile
// in through the browser, or an error when the CLI is missing or predates
// `aws login`.
func loginCommand(cfg Config) (*exec.Cmd, error) {
	out, err := exec.Command("aws", "--version").Output()
	if err != nil {
		return nil, errors.New("aws CLI not found; install AWS CLI 2.32 or newer")
	}
	if v, ok := strings.CutPrefix(string(out), "aws-cli/"); ok {
		v, _, _ = strings.Cut(v, " ")
		var major, minor int
		if n, _ := fmt.Sscanf(v, "%d.%d", &major, &minor); n == 2 && (major < 2 || major == 2 && minor < 32) {
			return nil, fmt.Errorf("aws CLI %s has no \"aws login\"; upgrade to 2.32 or newer", v)
		}
	}
	return exec.Command("aws", "login", "--profile", cfg.profile(), "--region", cfg.Region), nil
}
