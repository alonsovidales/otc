// Off The Cloud - Android app (issue #88). A 1:1 port of app/ios, kept in
// its own directory so the two can be worked on independently.
pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}
rootProject.name = "OffTheCloud"
include(":app")
