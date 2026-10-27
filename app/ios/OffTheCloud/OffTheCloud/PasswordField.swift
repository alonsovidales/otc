// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI
import UIKit

/// A password field with an eye button that shows or hides what is typed
/// (app.shell.password_show / password_hide), AutoFill's content type, and
/// the return key going on: Next to the field after it, Go to submit. The
/// welcome screen, the "can't connect" card and Settings; Settings › Wi-Fi
/// keeps its own field, which stays out of AutoFill. As Android's
/// ui/common/PasswordField.kt.
struct PasswordField: View {
    let title: String
    @Binding var text: String
    /// .password, or .newPassword where one is chosen.
    var contentType: UITextContentType = .password
    var submitLabel: SubmitLabel = .go
    var onSubmit: () -> Void = {}
    /// Focus from outside: the field before this one's Next lands here.
    var focus: FocusState<Bool>.Binding?

    @State private var shown = false
    @FocusState private var ownFocus: Bool

    var body: some View {
        HStack(spacing: 8) {
            Group {
                if shown {
                    TextField(title, text: $text)
                } else {
                    SecureField(title, text: $text)
                }
            }
            .textContentType(contentType)
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
            .submitLabel(submitLabel)
            .onSubmit(onSubmit)
            .focused(focus ?? $ownFocus)
            Button {
                // The other field takes the focus the toggled one had.
                let focused = focus?.wrappedValue ?? ownFocus
                shown.toggle()
                if focused {
                    DispatchQueue.main.async {
                        if let focus { focus.wrappedValue = true } else { ownFocus = true }
                    }
                }
            } label: {
                Image(systemName: shown ? "eye.slash" : "eye")
                    // Grey, not the borderless button's accent tint.
                    .foregroundStyle(Color.secondary)
            }
            .buttonStyle(.borderless)
            .accessibilityLabel(shown ? S.appShellPasswordHide : S.appShellPasswordShow)
        }
    }
}
