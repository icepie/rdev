# File picker target i18n check

Verified the new `files.uploadDeviceChanged` message exists exactly once in both English and Chinese dictionaries. The i18n fallback continues to return the English string when a key is absent in another locale, and the picker regression uses the key rather than hard-coded copy.

The target lock applies only to ordinary new uploads. Re-selecting a persisted failed upload remains bound to its task's original device/path, which is necessary for safe resume semantics.
