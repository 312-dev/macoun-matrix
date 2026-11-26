/*
 * macoun_ext.m - anisette headers from AOSKit, and an offline NAC probe.
 *
 * Added 2026-11-25 (macoun-relay). Not part of upstream corten-matrix.
 * Licensed under the GNU AGPL v3 or later, like the rest of this tree (see LICENSE).
 *
 * The anisette calls follow omnisette's aos_kit.rs (OpenBubbles/apple-private-apis):
 * [AOSUtilities retrieveOTPHeadersForDSID:@"-2"] for the OTP and machine id, plus
 * AOSUtilities/AKDevice for the device fields. Unlike aos_kit.rs, every object is
 * type-checked before use, so a changed framework returns an error instead of crashing.
 */

#import <Foundation/Foundation.h>
#import <dlfcn.h>
#import <objc/runtime.h>
#import <objc/message.h>
#include <stdlib.h>
#include <string.h>
#include "macoun_ext.h"

static int fail(char **out_err, int code, NSString *msg) {
    if (out_err) *out_err = strdup(msg.UTF8String ?: "unknown error");
    return code;
}

static id callClass(Class cls, NSString *selName) {
    SEL sel = NSSelectorFromString(selName);
    if (!cls || ![cls respondsToSelector:sel]) return nil;
    return ((id(*)(id, SEL))objc_msgSend)(cls, sel);
}

static id callObj(id obj, NSString *selName) {
    SEL sel = NSSelectorFromString(selName);
    if (!obj || ![obj respondsToSelector:sel]) return nil;
    return ((id(*)(id, SEL))objc_msgSend)(obj, sel);
}

static NSString *asString(id v) {
    return [v isKindOfClass:[NSString class]] && [(NSString *)v length] > 0 ? v : nil;
}

int macoun_anisette_json(char **out_json, char **out_err) {
    @autoreleasepool {
        *out_json = NULL;
        if (out_err) *out_err = NULL;

        if (!dlopen("/System/Library/PrivateFrameworks/AOSKit.framework/AOSKit", RTLD_NOW))
            return fail(out_err, 1, @"failed to load AOSKit.framework");
        if (!dlopen("/System/Library/PrivateFrameworks/AuthKit.framework/AuthKit", RTLD_NOW))
            return fail(out_err, 1, @"failed to load AuthKit.framework");

        Class utils = NSClassFromString(@"AOSUtilities");
        Class akDevice = NSClassFromString(@"AKDevice");
        if (!utils || !akDevice)
            return fail(out_err, 2, @"AOSUtilities or AKDevice class not found");

        SEL otpSel = NSSelectorFromString(@"retrieveOTPHeadersForDSID:");
        if (![utils respondsToSelector:otpSel])
            return fail(out_err, 3, @"AOSUtilities has no retrieveOTPHeadersForDSID:");
        id otp = ((id(*)(id, SEL, id))objc_msgSend)(utils, otpSel, @"-2");
        if (![otp isKindOfClass:[NSDictionary class]])
            return fail(out_err, 4, @"retrieveOTPHeadersForDSID: returned no dictionary");

        id device = callClass(akDevice, @"currentDevice");

        // Header name -> value. AOSKit's own key names (X-Apple-MD, X-Apple-MD-M) are
        // mapped to the X-Apple-I-* names Apple's GSA endpoints expect, as aos_kit.rs does.
        NSMutableDictionary *out = [NSMutableDictionary dictionary];
        NSMutableArray *missing = [NSMutableArray array];
        void (^put)(NSString *, id) = ^(NSString *name, id value) {
            NSString *s = asString(value);
            if (s) out[name] = s; else [missing addObject:name];
        };

        put(@"X-Apple-I-MD", ((NSDictionary *)otp)[@"X-Apple-MD"]);
        put(@"X-Apple-I-MD-M", ((NSDictionary *)otp)[@"X-Apple-MD-M"]);
        put(@"X-Apple-SRL-NO", callClass(utils, @"machineSerialNumber"));
        put(@"X-Apple-I-MD-LU", callObj(device, @"localUserUUID"));
        put(@"X-Apple-Locale", callObj(callObj(device, @"locale"), @"localeIdentifier"));
        put(@"X-Mme-Client-Info", callObj(device, @"serverFriendlyDescription"));
        put(@"X-Mme-Device-Id", callObj(device, @"uniqueDeviceIdentifier"));

        // The OTP is time-based, so the Mac's clock at generation time goes with it.
        NSDateFormatter *fmt = [[NSDateFormatter alloc] init];
        fmt.locale = [NSLocale localeWithLocaleIdentifier:@"en_US_POSIX"];
        fmt.timeZone = [NSTimeZone timeZoneForSecondsFromGMT:0];
        fmt.dateFormat = @"yyyy-MM-dd'T'HH:mm:ss'Z'";
        put(@"X-Apple-I-Client-Time", [fmt stringFromDate:[NSDate date]]);
        put(@"X-Apple-I-TimeZone", [[NSTimeZone localTimeZone] abbreviation]);

        if (missing.count > 0)
            return fail(out_err, 5, [NSString stringWithFormat:@"missing anisette fields: %@",
                                     [missing componentsJoinedByString:@", "]]);

        NSError *jsonErr = nil;
        NSData *json = [NSJSONSerialization dataWithJSONObject:out options:NSJSONWritingSortedKeys error:&jsonErr];
        if (!json)
            return fail(out_err, 6, [NSString stringWithFormat:@"JSON encoding failed: %@", jsonErr.localizedDescription]);

        char *buf = malloc(json.length + 1);
        memcpy(buf, json.bytes, json.length);
        buf[json.length] = '\0';
        *out_json = buf;
        return 0;
    }
}

int macoun_nac_probe(char **out_err) {
    @autoreleasepool {
        if (out_err) *out_err = NULL;
        if (!dlopen("/System/Library/PrivateFrameworks/AppleAccount.framework/AppleAccount", RTLD_NOW))
            return fail(out_err, 1, @"failed to load AppleAccount.framework");
        Class cls = NSClassFromString(@"AAAbsintheContext");
        if (!cls)
            return fail(out_err, 4, @"AAAbsintheContext class not found");

        // Same filter as discover_nac_selectors() in validation_data.m: two-colon
        // "*:error:" instance methods, one returning BOOL, two returning objects.
        unsigned int n = 0;
        Method *methods = class_copyMethodList(cls, &n);
        int boolCount = 0, objCount = 0;
        for (unsigned int i = 0; i < n; i++) {
            const char *name = sel_getName(method_getName(methods[i]));
            const char *enc = method_getTypeEncoding(methods[i]);
            if (!name || !enc) continue;
            size_t len = strlen(name);
            if (len < 7 || strcmp(name + len - 7, ":error:") != 0) continue;
            int colons = 0;
            for (const char *p = name; *p; p++) if (*p == ':') colons++;
            if (colons != 2) continue;
            if (enc[0] == 'B' || enc[0] == 'c') boolCount++;
            else if (enc[0] == '@') objCount++;
        }
        free(methods);
        if (boolCount < 1 || objCount < 2)
            return fail(out_err, 21, [NSString stringWithFormat:
                @"AAAbsintheContext selector shape changed (bool=%d, object=%d; need >=1 and >=2)",
                boolCount, objCount]);
        return 0;
    }
}
