/*
 * macoun_ext.h - additions to corten-matrix tools/nac-relay for macoun-relay.
 *
 * Added 2026-11-25 (macoun-relay). Not part of upstream corten-matrix.
 * Licensed under the GNU AGPL v3 or later, like the rest of this tree (see LICENSE).
 */

#ifndef MACOUN_EXT_H
#define MACOUN_EXT_H

/*
 * Fill *out_json with a malloc'd JSON object mapping Apple anisette header
 * names to values, produced by this Mac's AOSKit/AuthKit (the same calls as
 * omnisette's AOSKitAnisetteProvider). Caller frees *out_json.
 * Returns 0 on success; on failure returns non-zero and sets *out_err to a
 * malloc'd message that names what was missing (never a header value).
 */
int macoun_anisette_json(char **out_json, char **out_err);

/*
 * Local, offline check that the NAC code path can still bind: loads
 * AppleAccount.framework, finds AAAbsintheContext and checks that the
 * "*:error:" selector shape validation_data.m relies on is still there.
 * Makes no network calls. Returns 0 if the shape matches; otherwise
 * non-zero with a malloc'd *out_err.
 */
int macoun_nac_probe(char **out_err);

#endif
