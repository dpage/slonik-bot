// The selectors and page texts the extension matches in Meet, copied from
// internal/meet/selectors.go. TestExtensionSelectorsMatch checks every line
// against the Go constants, so keep one key per line and double quotes.
globalThis.SlonikSelectors = {
  selCaptionsOn: "button[aria-label=\"Turn on captions\"]",
  selCaptionsOff: "button[aria-label=\"Turn off captions\"]",
  selCaptionsMenu: "button[aria-label=\"Captions and translation\"]",
  selCaptionsDialog: "[role=\"dialog\"][aria-label=\"Captions and translation\"]",
  selChatButton: "button[aria-label=\"Chat with everyone\"]",
  selChatInput: "textarea[aria-label=\"Send a message\"]",
  selLeave: "button[aria-label=\"Leave call\"]",
  selParticipantMenus: "[aria-label^=\"More options for \"]",
  participantMenuPrefix: "More options for ",
  textsEnded: ["The call ended because everyone left", "Your host ended the meeting for everyone", "You left the meeting"],
  textsRemoved: ["You've been removed from the meeting"],
};
