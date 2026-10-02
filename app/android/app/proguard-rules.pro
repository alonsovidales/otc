# protobuf-lite generated classes are reflected on by the runtime.
-keep class cloud.offthe.otc.proto.** { *; }

# Issue #129: protobuf-lite finds message fields by name (seconds_, nanos_...),
# including the well-known types it ships itself (Timestamp, Duration) - R8
# renaming them broke every message with a timestamp in the release build.
-keep class * extends com.google.protobuf.GeneratedMessageLite { <fields>; }
