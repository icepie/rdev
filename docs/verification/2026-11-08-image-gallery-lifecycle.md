# Image gallery idle work

Inspection found that each thumbnail in a message/composer rebuilt and filtered the complete image gallery even while closed, and mounted an inactive ImageLightbox with its modal hooks. For n attachments the parent created n arrays of n entries per render.

Chat now builds one shared gallery per message render and memoizes the composer gallery by its attachment list. Closed thumbnails only resolve their own image; the gallery is filtered and the modal mounted when opened. Retry keys use the attempt number rather than copying the full base64 source into a React key.

Existing gallery tests cover image replacement, reordering, removed selections, missing triggers, and thumbnail retry. Added keyboard regression verifies navigating away from the clicked image, Escape dismissal, focus return to the original trigger after modal unmount, and reopening at the clicked image.

Validation: typecheck passed; 73 Web files / 281 tests passed; production build and diff whitespace check passed. This establishes reduced allocations and inactive component work by code inspection, not a measured browser latency or heap improvement. Full browser performance baselines remain outstanding.
