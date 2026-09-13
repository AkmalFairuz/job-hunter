package jobbot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/bwmarrin/discordgo"
)

type ManualRunner interface {
	RunSubscription(context.Context, string, string, ManualRunOptions) error
}

type DiscordService struct {
	session       *discordgo.Session
	repository    Repository
	runner        ManualRunner
	applicationID string
	allowedGuilds map[string]struct{}
	logger        *slog.Logger
}

func NewDiscordService(config DiscordConfig, repository Repository, logger *slog.Logger) (*DiscordService, error) {
	session, err := discordgo.New("Bot " + config.Token)
	if err != nil {
		return nil, fmt.Errorf("create Discord session: %w", err)
	}
	session.Identify.Intents = discordgo.IntentsGuilds
	session.Client.Timeout = config.HTTPTimeout
	return &DiscordService{
		session: session, repository: repository, applicationID: config.ApplicationID,
		allowedGuilds: guildSet(config.AllowedGuildIDs),
		logger:        logger,
	}, nil
}

func (service *DiscordService) SetRunner(runner ManualRunner) { service.runner = runner }

func (service *DiscordService) Start() error {
	service.session.AddHandler(service.handleInteraction)
	if err := service.syncCommand(); err != nil {
		return err
	}
	if err := service.session.Open(); err != nil {
		return fmt.Errorf("open Discord session: %w", err)
	}
	return nil
}

func (service *DiscordService) Close() error { return service.session.Close() }

func (service *DiscordService) syncCommand() error {
	// Remove an older global /jobs command, if present. Whitelisted guild
	// commands are registered per guild so the command is not exposed elsewhere.
	globalCommands, err := service.session.ApplicationCommands(service.applicationID, "")
	if err != nil {
		return fmt.Errorf("list global Discord commands: %w", err)
	}
	for _, command := range globalCommands {
		if command.Name == "jobs" {
			if err := service.session.ApplicationCommandDelete(service.applicationID, "", command.ID); err != nil {
				return fmt.Errorf("remove global /jobs command: %w", err)
			}
		}
	}
	for guildID := range service.allowedGuilds {
		if err := service.syncGuildCommand(guildID); err != nil {
			return err
		}
	}
	return nil
}

func (service *DiscordService) syncGuildCommand(guildID string) error {
	commands, err := service.session.ApplicationCommands(service.applicationID, guildID)
	if err != nil {
		return fmt.Errorf("list Discord commands for guild %s: %w", guildID, err)
	}
	definition := jobsCommand()
	for _, command := range commands {
		if command.Name == definition.Name {
			if _, err := service.session.ApplicationCommandEdit(service.applicationID, guildID, command.ID, definition); err != nil {
				return fmt.Errorf("update /jobs command for guild %s: %w", guildID, err)
			}
			return nil
		}
	}
	if _, err := service.session.ApplicationCommandCreate(service.applicationID, guildID, definition); err != nil {
		return fmt.Errorf("create /jobs command for guild %s: %w", guildID, err)
	}
	return nil
}

func jobsCommand() *discordgo.ApplicationCommand {
	permission := int64(discordgo.PermissionManageGuild)
	minimumHoursOld := float64(0)
	stringOption := func(name, description string, required bool) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: name, Description: description, Required: required}
	}
	channelOption := func(required bool) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{
			Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "Channel that receives notifications", Required: required,
			ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews},
		}
	}
	subcommand := func(name, description string, options ...*discordgo.ApplicationCommandOption) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommand, Name: name, Description: description, Options: options}
	}
	return &discordgo.ApplicationCommand{
		Name: "jobs", Description: "Configure LinkedIn vacancy notifications",
		DefaultMemberPermissions: &permission,
		Options: []*discordgo.ApplicationCommandOption{
			subcommand("add", "Add a named job search", stringOption("name", "Unique search name", true), channelOption(true),
				stringOption("query", "LinkedIn search query", true), stringOption("locations", "Comma-separated LinkedIn locations", true),
				stringOption("ai_prompt", "Optional additional AI filtering criteria", false)),
			subcommand("update", "Update a named job search", stringOption("name", "Existing search name", true), channelOption(false),
				stringOption("query", "New LinkedIn search query", false), stringOption("locations", "New comma-separated locations", false),
				stringOption("ai_prompt", "New additional AI criteria", false),
				&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionBoolean, Name: "clear_ai_prompt", Description: "Remove the saved AI criteria"}),
			subcommand("remove", "Delete a named job search", stringOption("name", "Existing search name", true)),
			subcommand("list", "List saved job searches", channelOption(false)),
			subcommand("enable", "Enable a named job search", stringOption("name", "Existing search name", true)),
			subcommand("disable", "Disable a named job search", stringOption("name", "Existing search name", true)),
			subcommand("run", "Run a named job search now", stringOption("name", "Existing search name", true),
				&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionInteger, Name: "hours_old", Description: "Only include jobs posted within this many hours; defaults to the scheduler setting", MinValue: &minimumHoursOld}),
		},
	}
}

func (service *DiscordService) handleInteraction(_ *discordgo.Session, interaction *discordgo.InteractionCreate) {
	if !guildAllowed(service.allowedGuilds, interaction.GuildID) {
		if interaction.Type == discordgo.InteractionApplicationCommand {
			service.respond(interaction, "This Discord server is not allowed to use jobbot.")
		}
		return
	}
	switch interaction.Type {
	case discordgo.InteractionApplicationCommand:
		if interaction.ApplicationCommandData().Name == "jobs" {
			service.handleJobs(interaction)
		}
	}
}

func (service *DiscordService) handleJobs(interaction *discordgo.InteractionCreate) {
	if interaction.GuildID == "" || interaction.Member == nil || interaction.Member.User == nil {
		service.respond(interaction, "This command can only be used in a server.")
		return
	}
	if !canManageGuild(interaction.Member) {
		service.respond(interaction, "You need Manage Server permission to use /jobs commands.")
		return
	}
	options := interaction.ApplicationCommandData().Options
	if len(options) != 1 {
		service.respond(interaction, "Invalid /jobs command.")
		return
	}
	subcommand := options[0]
	values := optionMap(subcommand.Options)
	name := stringValue(values, "name")
	ctx := context.Background()
	var message string
	var err error
	switch subcommand.Name {
	case "add":
		input := SubscriptionInput{
			GuildID: interaction.GuildID, ChannelID: channelValue(values, "channel"), Name: name,
			Query: stringValue(values, "query"), Locations: parseLocations(stringValue(values, "locations")),
			AIPrompt: stringValue(values, "ai_prompt"), CreatedBy: interaction.Member.User.ID,
		}
		if err = validateSubscription(input); err == nil {
			err = service.validateNotificationChannel(input.ChannelID)
		}
		if err == nil {
			_, err = service.repository.CreateSubscription(ctx, input)
		}
		message = fmt.Sprintf("Added job search **%s** for <#%s>.", input.Name, input.ChannelID)
	case "update":
		var subscription Subscription
		subscription, err = service.repository.GetSubscription(ctx, interaction.GuildID, name)
		if err == nil {
			if value := channelValue(values, "channel"); value != "" {
				subscription.ChannelID = value
			}
			if value := stringValue(values, "query"); value != "" {
				subscription.Query = strings.TrimSpace(value)
			}
			if _, present := values["locations"]; present {
				subscription.Locations = parseLocations(stringValue(values, "locations"))
			}
			if boolValue(values, "clear_ai_prompt") {
				subscription.AIPrompt = ""
			} else if _, present := values["ai_prompt"]; present {
				subscription.AIPrompt = strings.TrimSpace(stringValue(values, "ai_prompt"))
			}
			validation := SubscriptionInput{GuildID: subscription.GuildID, ChannelID: subscription.ChannelID, Name: subscription.Name, Query: subscription.Query, Locations: subscription.Locations, CreatedBy: subscription.CreatedBy}
			if err = validateSubscription(validation); err == nil {
				err = service.validateNotificationChannel(subscription.ChannelID)
			}
			if err == nil {
				err = service.repository.UpdateSubscription(ctx, subscription)
			}
		}
		message = fmt.Sprintf("Updated job search **%s**.", name)
	case "remove":
		err = service.repository.DeleteSubscription(ctx, interaction.GuildID, name)
		message = fmt.Sprintf("Removed job search **%s**.", name)
	case "enable", "disable":
		enabled := subcommand.Name == "enable"
		err = service.repository.SetSubscriptionEnabled(ctx, interaction.GuildID, name, enabled)
		message = fmt.Sprintf("Job search **%s** is now %s.", name, map[bool]string{true: "enabled", false: "disabled"}[enabled])
	case "list":
		var subscriptions []Subscription
		subscriptions, err = service.repository.ListSubscriptions(ctx, interaction.GuildID, channelValue(values, "channel"))
		message = formatSubscriptionList(subscriptions)
	case "run":
		manualOptions := ManualRunOptions{}
		if option := values["hours_old"]; option != nil {
			value := int(option.IntValue())
			manualOptions.HoursOld = &value
		}
		service.runNow(interaction, name, manualOptions)
		return
	default:
		err = errors.New("unknown subcommand")
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			message = fmt.Sprintf("Job search **%s** was not found.", name)
		} else {
			service.logger.Error("handle Discord command", "command", subcommand.Name, "error", err)
			message = "The job search could not be updated. Check the bot logs."
		}
	}
	service.respond(interaction, message)
}

func (service *DiscordService) runNow(interaction *discordgo.InteractionCreate, name string, options ManualRunOptions) {
	if service.runner == nil {
		service.respond(interaction, "The scheduler is not ready.")
		return
	}
	err := service.session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	})
	if err != nil {
		service.logger.Error("defer manual run response", "error", err)
		return
	}
	message := fmt.Sprintf("Finished running **%s**.", name)
	if options.HoursOld != nil {
		message = fmt.Sprintf("Finished running **%s** with a maximum age of **%d hours**.", name, *options.HoursOld)
	}
	if err := service.runner.RunSubscription(context.Background(), interaction.GuildID, name, options); err != nil {
		if errors.Is(err, ErrNotFound) {
			message = fmt.Sprintf("Job search **%s** was not found.", name)
		} else {
			service.logger.Error("manual job search", "name", name, "error", err)
			message = "The search finished with errors. Check the bot logs."
		}
	}
	_, _ = service.session.InteractionResponseEdit(interaction.Interaction, &discordgo.WebhookEdit{Content: &message})
}

func canManageGuild(member *discordgo.Member) bool {
	if member == nil {
		return false
	}
	return member.Permissions&discordgo.PermissionManageGuild != 0 || member.Permissions&discordgo.PermissionAdministrator != 0
}

func (service *DiscordService) respond(interaction *discordgo.InteractionCreate, message string) {
	err := service.session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: truncateRunes(message, 1900), Flags: discordgo.MessageFlagsEphemeral, AllowedMentions: &discordgo.MessageAllowedMentions{}},
	})
	if err != nil {
		service.logger.Error("respond to Discord interaction", "error", err)
	}
}

func (service *DiscordService) SendJob(_ context.Context, notification Notification) error {
	_, err := service.session.ChannelMessageSendComplex(notification.ChannelID, buildJobMessage(notification))
	if err != nil {
		safeToRetry := false
		var restError *discordgo.RESTError
		if errors.As(err, &restError) && restError.Response != nil {
			safeToRetry = restError.Response.StatusCode >= 400 && restError.Response.StatusCode < 500
		}
		return &DeliveryError{Err: err, SafeToRetry: safeToRetry}
	}
	return nil
}

func (service *DiscordService) validateNotificationChannel(channelID string) error {
	if service.session.State == nil || service.session.State.User == nil {
		return errors.New("Discord bot identity is unavailable")
	}
	permissions, err := service.session.UserChannelPermissions(service.session.State.User.ID, channelID)
	if err != nil {
		return fmt.Errorf("read bot channel permissions: %w", err)
	}
	required := int64(discordgo.PermissionViewChannel | discordgo.PermissionSendMessages | discordgo.PermissionEmbedLinks)
	if permissions&discordgo.PermissionAdministrator == 0 && permissions&required != required {
		return errors.New("bot needs View Channel, Send Messages, and Embed Links permissions in the selected channel")
	}
	return nil
}

func buildJobMessage(notification Notification) *discordgo.MessageSend {
	posted := "Unknown"
	if notification.DatePosted != nil {
		posted = fmt.Sprintf("<t:%d:D>", notification.DatePosted.Unix())
	}
	location := notification.Location
	if location == "" {
		location = "Unknown"
	}
	postedLabel := "Posted"
	if notification.Reposted {
		postedLabel = "Reposted"
	}
	embed := &discordgo.MessageEmbed{
		Title: truncateRunes(notification.Title, 256), Description: truncateRunes(notification.AIOverview, 4096), Color: 0x0A66C2,
		Author: &discordgo.MessageEmbedAuthor{Name: truncateRunes(notification.CompanyName, 256)},
		Fields: []*discordgo.MessageEmbedField{{Name: "Location", Value: truncateRunes(location, 1024), Inline: true}, {Name: postedLabel, Value: posted, Inline: true}},
	}
	if validHTTPURL(notification.CompanyLogoURL) {
		embed.Thumbnail = &discordgo.MessageEmbedThumbnail{URL: notification.CompanyLogoURL}
	}
	return &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{embed}, AllowedMentions: &discordgo.MessageAllowedMentions{},
		Components: jobLinkComponents(notification.JobURL),
	}
}

func jobLinkComponents(jobURL string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: "Details", Style: discordgo.LinkButton, URL: jobURL},
	}}}
}

func optionMap(options []*discordgo.ApplicationCommandInteractionDataOption) map[string]*discordgo.ApplicationCommandInteractionDataOption {
	result := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))
	for _, option := range options {
		result[option.Name] = option
	}
	return result
}

func stringValue(options map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	if option := options[name]; option != nil {
		return option.StringValue()
	}
	return ""
}
func channelValue(options map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	if option := options[name]; option != nil {
		return option.ChannelValue(nil).ID
	}
	return ""
}
func boolValue(options map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) bool {
	if option := options[name]; option != nil {
		return option.BoolValue()
	}
	return false
}

func formatSubscriptionList(subscriptions []Subscription) string {
	if len(subscriptions) == 0 {
		return "No job searches are configured."
	}
	var builder strings.Builder
	builder.WriteString("**Configured job searches**\n")
	for _, subscription := range subscriptions {
		state := "enabled"
		if !subscription.Enabled {
			state = "disabled"
		}
		fmt.Fprintf(&builder, "- **%s** (%s) → <#%s> — `%s` — %s\n", escapeMarkdown(subscription.Name), state, subscription.ChannelID, strings.ReplaceAll(subscription.Query, "`", "'"), strings.Join(subscription.Locations, ", "))
	}
	return truncateRunes(builder.String(), 1900)
}

func validHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func escapeMarkdown(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "`", "\\`", "~", "\\~", "|", "\\|")
	return truncateRunes(replacer.Replace(strings.TrimSpace(value)), 200)
}
