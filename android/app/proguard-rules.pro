# The gomobile bindings call into Java by name (Seq, the Folders and
# Secrets callbacks), so R8 must keep them as they are.
-keep class go.** { *; }
-keep class online.useoros.drive.mobile.** { *; }
