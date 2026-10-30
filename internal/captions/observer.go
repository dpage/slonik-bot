package captions

import _ "embed" // for ObserverJS

// BindingName is the page function observer.js calls with each Event, as JSON.
const BindingName = "__slonikCaption"

// ObserverJS is the script that watches Meet's captions region and reports
// caption blocks through the BindingName binding. Running it twice on a page
// is harmless.
//
//go:embed observer.js
var ObserverJS string
