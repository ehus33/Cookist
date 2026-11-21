import SwiftUI

private let orange = Color(red: 0.84, green: 0.31, blue: 0.12)
private let cream = Color(red: 1, green: 0.97, blue: 0.94)

@main struct CookistApp: App {
    var body: some Scene {
        WindowGroup {
            ContentView()
                .tint(orange)
        }
    }
}

private struct ContentView: View {
    var body: some View {
        VStack(spacing: 24) {
            VStack(spacing: 4) {
                Text("Cookist")
                    .font(.largeTitle.bold())
                    .foregroundStyle(orange)
                Text("A Machi service")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
            }

            VStack(spacing: 10) {
                Image(systemName: "sparkles")
                    .font(.title)
                    .foregroundStyle(orange)
                Text("A fresh start")
                    .font(.title2.bold())
                Text("This app is ready for what comes next.")
                    .font(.body)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
            }
            .frame(maxWidth: .infinity)
            .padding(28)
            .background(.white.opacity(0.8), in: RoundedRectangle(cornerRadius: 20))
        }
        .padding(24)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(cream.ignoresSafeArea())
    }
}
