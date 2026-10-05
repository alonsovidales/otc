// SPDX-License-Identifier: AGPL-3.0-or-later

#include <cstdio>
#include <exception>
#include <opencv2/objdetect.hpp>

#include "model_probe.h"

// Loads both models the way gocv does, but with OpenCV's exceptions caught:
// gocv's own constructors let one cross into Go, which aborts the process.
// The models are freed when the Ptrs go out of scope.
int otc_probe_face_models(const char* detector, const char* recognizer, char* errbuf, int errlen) {
    try {
        // The same calls as gocv's FaceDetectorYN_Create_WithParams and
        // FaceRecognizerSF_Create_WithParams.
        cv::Ptr<cv::FaceDetectorYN> d = cv::FaceDetectorYN::create(cv::String(detector), cv::String(""), cv::Size(320, 320), 0.6f, 0.3f, 5000, 0, 0);
        cv::Ptr<cv::FaceRecognizerSF> r = cv::FaceRecognizerSF::create(cv::String(recognizer), cv::String(""), 0, 0);
        return (d.empty() || r.empty()) ? 2 : 0;
    } catch (const cv::Exception& e) {
        snprintf(errbuf, errlen, "%s", e.what());
    } catch (const std::exception& e) {
        snprintf(errbuf, errlen, "%s", e.what());
    } catch (...) {
        snprintf(errbuf, errlen, "unknown C++ exception");
    }
    return 1;
}
