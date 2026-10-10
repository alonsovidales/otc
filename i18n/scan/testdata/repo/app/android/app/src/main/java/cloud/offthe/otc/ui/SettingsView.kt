// Fixture: the Android surface.
package cloud.offthe.otc.ui

import android.util.Log
import android.widget.Toast
import androidx.compose.material3.Text

private const val TAG = "SettingsView"

@Deprecated("Deprecated in Java")
@Composable
fun SettingsView(count: Int, vm: SettingsViewModel) {
    Text("Storage")
    Text(text = "Used $count of ${vm.total} on the disk")
    Text("${count}")
    Icon(Icons.Default.Delete, contentDescription = "Delete the folder")
    Caption("Photos are kept on the device.")
    Toast.makeText(context, "Saved to the device", Toast.LENGTH_SHORT).show()
    Log.d(TAG, "Saving the settings now")
    val prefs = getSharedPreferences("otc_settings", 0)
    prefs.getString("local_endpoint", null)
    if (vm.state == "Some State Value") return
    when (vm.kind) {
        "Kind With Spaces" -> {}
    }
    val pairs = mapOf("Header Key Name" to 1)
    Text("Kept for the record") // i18n-ignore: fixture for the escape hatch
    val raw = """
        A raw string
        across lines.
    """
}

class SettingsViewModel {
    var error: String? = null
    val total = 3
    var state = ""
    var kind = ""
    fun fail() {
        error = "Could not save the settings."
        _toast.value = "Saved"
        throw IOException("The device sent an answer this app could not read")
    }
}
