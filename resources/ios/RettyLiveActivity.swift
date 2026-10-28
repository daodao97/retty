import ActivityKit
import SwiftUI
import WidgetKit

@available(iOSApplicationExtension 16.2, *)
struct RettyLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: MyGoLiveActivityAttributes.self) { context in
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 8) {
                    Image(systemName: symbol(context.state.status))
                        .foregroundStyle(color(context.state.status))
                    Text(context.state.title).font(.headline).lineLimit(1).privacySensitive()
                    Spacer()
                    Text("Retty").font(.caption).foregroundStyle(.secondary)
                }
                Text(context.isStale ? "连接暂时中断，打开 Retty 刷新" : context.state.subtitle)
                    .font(.subheadline).lineLimit(2)
                if let progress = context.state.progress {
                    ProgressView(value: progress).tint(color(context.state.status))
                }
                HStack {
                    Text("点按返回会话")
                    Spacer()
                    Text(Date(timeIntervalSince1970: Double(context.state.updatedAt)), style: .time)
                }.font(.caption2).foregroundStyle(.secondary)
            }
            .padding(16)
            .activityBackgroundTint(Color(.secondarySystemBackground))
            .activitySystemActionForegroundColor(Color.primary)
            .widgetURL(URL(string: context.state.url))
        } dynamicIsland: { context in
            DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    Label("Retty", systemImage: symbol(context.state.status))
                        .foregroundStyle(color(context.state.status))
                }
                DynamicIslandExpandedRegion(.trailing) {
                    if let progress = context.state.progress {
                        Text(progress, format: .percent.precision(.fractionLength(0))).monospacedDigit()
                    }
                }
                DynamicIslandExpandedRegion(.bottom) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(context.state.title).font(.headline).lineLimit(1).privacySensitive()
                        Text(context.isStale ? "打开 Retty 刷新状态" : context.state.subtitle).font(.subheadline)
                    }.frame(maxWidth: .infinity, alignment: .leading)
                }
            } compactLeading: {
                Image(systemName: symbol(context.state.status)).foregroundStyle(color(context.state.status))
            } compactTrailing: {
                Text(shortLabel(context.state.status)).font(.caption2)
            } minimal: {
                Image(systemName: symbol(context.state.status)).foregroundStyle(color(context.state.status))
            }
            .widgetURL(URL(string: context.state.url))
            .keylineTint(color(context.state.status))
        }
    }
}

private func symbol(_ status: String) -> String {
    switch status {
    case "unknown": return "arrow.triangle.2.circlepath"
    case "waiting": return "hand.raised.fill"
    case "completed", "done", "ended": return "checkmark.circle.fill"
    case "failed", "error": return "exclamationmark.triangle.fill"
    default: return "terminal.fill"
    }
}
private func color(_ status: String) -> Color {
    switch status {
    case "waiting": return .orange
    case "completed", "done", "ended": return .green
    case "failed", "error": return .red
    default: return .cyan
    }
}
private func shortLabel(_ status: String) -> String {
    switch status {
    case "ready": return "就绪"
    case "unknown": return "同步"
    case "waiting": return "等待"
    case "completed", "done", "ended": return "完成"
    case "failed", "error": return "异常"
    default: return "运行"
    }
}

@main
struct RettyActivityBundle: WidgetBundle {
    var body: some Widget {
        if #available(iOSApplicationExtension 16.2, *) { RettyLiveActivity() }
    }
}
