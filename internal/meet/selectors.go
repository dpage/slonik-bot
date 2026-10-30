// Package meet drives Google Meet's web page: joining, captions, chat,
// participants, and noticing when the call is over.
package meet

import "regexp"

// Every CSS selector, XPath and user-visible string the bot matches lives in
// this file, because Meet changes its markup without notice and a fix should
// touch one place. The fake page in fakemeet mirrors these.

const (
	// Pre-join.
	selMicOff                = `[aria-label*="Turn off microphone"]`
	selCameraOff             = `[aria-label*="Turn off camera"]`
	xpJoin                   = `//button[.//span[text()="Join now" or text()="Ask to join" or text()="Join the call now" or text()="Join anyway" or text()="Join here too" or text()="Ask to join anyway" or text()="Switch here"]]`
	xpContinueWithoutDevices = `//button[.//span[text()="Continue without microphone and camera"]]`
	selNameInput             = `input[type="text"][aria-label="Your name"], input[jsname][type="text"]`
	xpSignInHeader           = `//h1[text()="Sign in"]`
	signInURLPrefix          = "https://accounts.google.com/"
	selConsentOK             = `div[aria-modal="true"][role="dialog"] button[data-mdc-dialog-action="ok"], div[aria-modal="true"][role="alertdialog"] button[data-mdc-dialog-action="ok"]`
	xpGotIt                  = `//button[.//span[text()="Got it"]]`

	// In call.
	selShareScreen        = `button[aria-label*="Share screen"], [aria-label*="Present now"]`
	selCaptionsOn         = `button[aria-label="Turn on captions"]`
	selCaptionsOff        = `button[aria-label="Turn off captions"]`
	selCaptionsMenu       = `button[aria-label="Captions and translation"]`
	selCaptionsRegion     = `[role="region"][aria-label="Captions"], div[role="region"][tabindex="0"]`
	selMoreOptions        = `button[aria-label="More options"]`
	xpSettings            = `//li[.//span[text()="Settings"]]`
	selCaptionsTab        = `button[aria-label="Captions"]`
	selLanguageItems      = `ul[aria-label="Meeting language"] li`
	selCloseDialog        = `button[aria-label="Close dialog"], button[aria-label="Close dialogue"]`
	selChatButton         = `button[aria-label="Chat with everyone"]`
	selChatInput          = `textarea[aria-label="Send a message"]`
	selAvatarCount        = `[data-avatar-count]`
	selPeopleButton       = `button[aria-label^="People - "]`
	selParticipantMenus   = `[aria-label^="More options for "]`
	participantMenuPrefix = "More options for "
	selLeave              = `button[aria-label="Leave call"]`
	xpJustLeave           = `//button[.//span[text()="Just leave the meeting"]]`
	selEndScreen          = `.roSPhc`
)

// Page texts, matched after normalising typographic apostrophes to '.
var (
	textsNotFound = []string{"Check your meeting code", "Invalid video call name", "Your meeting code has expired", "The meeting code you entered doesn't work"}
	textsBlocked  = []string{"You can't join this video call"}
	textsWaiting  = []string{"Asking to be let in", "Waiting for the host to join", "You'll join the call when someone lets you in", "Please wait until a meeting host brings you into the call", "You're in the waiting room"}
	textsDenied   = []string{"denied your request to join", "No one responded to your request to join the call"}
	textsRemoved  = []string{"You've been removed from the meeting"}
	textsEnded    = []string{"The call ended because everyone left", "Your host ended the meeting for everyone", "You left the meeting"}
	peopleLabel   = regexp.MustCompile(`^People - (\d+) joined$`)
	selfNames     = []string{"You"} // plus Options.BotName
)
