// SPDX-License-Identifier: AGPL-3.0-or-later

#ifndef OTC_MODEL_PROBE_H
#define OTC_MODEL_PROBE_H

#ifdef __cplusplus
extern "C" {
#endif

// 0: both models load. 1: OpenCV threw (the message in errbuf). 2: a
// model came back empty.
int otc_probe_face_models(const char* detector, const char* recognizer, char* errbuf, int errlen);

#ifdef __cplusplus
}
#endif

#endif
