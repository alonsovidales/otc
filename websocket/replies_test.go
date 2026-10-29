// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/devtext"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/supervisor"
)

// The device's replies moved into the catalog (i18n/strings/en/dev.err,
// dev.auth, dev.files, dev.social, dev.users and dev.device). A request
// without a language - a released app, a friend's device, the bridge -
// must get exactly the English it got before, byte for byte.

// plain is a rendering without the FSI/PDI marks the runtime puts around
// user-controlled arguments.
func plain(s string) string { return strings.NewReplacer("\u2068", "", "\u2069", "").Replace(s) }

// headEnglish is every reply key next to the text the device wrote before
// it moved into the catalog: the format string as it stood in the code,
// with the same arguments. Numbers stay under 1000 (English groups larger
// ones; those replies keep their old English through sayOr or
// devtext.WithEnglish, tested below).
var headEnglish = []struct {
	key  string
	got  i18n.Msg
	want string
}{
	// dev.err.action_failed is new: no English request ever shows it.
	{"dev.err.action_failed", i18n.DevErrActionFailed("boom"), "The device couldn't do that: boom"},
	{"dev.err.demo", i18n.DevErrDemo(), "This is a demo device: changes are turned off."},
	{"dev.err.error_detail", i18n.DevErrErrorDetail("boom"), fmt.Sprintf("Error: %s", "boom")},
	{"dev.err.internal", i18n.DevErrInternal(), "internal error"},
	{"dev.err.not_authenticated", i18n.DevErrNotAuthenticated(), "not authenticated"},
	// dev.err.not_in_container and dev.auth.first_password_* are new
	// (container mode): their English as first written.
	{"dev.err.not_in_container", i18n.DevErrNotInContainer(), "Not available in Docker: the server the device runs on looks after its disks and network."},
	{"dev.err.not_on_this_instance", i18n.DevErrNotOnThisInstance(), "not available on this instance"},
	{"dev.err.unknown_request", i18n.DevErrUnknownRequest(), "This device does not understand that request: its software is older than the app. Check for updates in Settings."},

	{"dev.auth.decrypt_failed", i18n.DevAuthDecryptFailed(), "unable to decrypt key material"},
	{"dev.auth.device_state_failed", i18n.DevAuthDeviceStateFailed(), "error checking device state"},
	{"dev.auth.first_password_home_only", i18n.DevAuthFirstPasswordHomeOnly(), "The owner password can only be chosen from your home network, at the device's own address and not through a proxy. You can also set it on the server: docker exec -i otc otc-entrypoint set-password, with the password as its input."},
	{"dev.auth.first_password_user_home_only", i18n.DevAuthFirstPasswordUserHomeOnly(), "This account's first password can only be chosen from your home network, at the account's own address and not through a proxy, or at its address on the bridge."},
	{"dev.auth.first_password_window_closed", i18n.DevAuthFirstPasswordWindowClosed(), "The 30 minutes to choose the owner password here have passed. Set it on the server instead: docker exec -i otc otc-entrypoint set-password, with the password as its input."},
	{"dev.auth.invalid_session", i18n.DevAuthInvalidSession(), "Invalid session"},
	{"dev.auth.keypair_failed", i18n.DevAuthKeypairFailed(), "error generating keypair"},
	{"dev.auth.no_public_key", i18n.DevAuthNoPublicKey(), "no public key was requested for this connection"},
	{"dev.auth.password_too_short", i18n.DevAuthPasswordTooShort(), "the password must have 8 characters or more"},
	{"dev.auth.public_key_failed", i18n.DevAuthPublicKeyFailed(), "error marshaling public key"},
	{"dev.auth.session_expired", i18n.DevAuthSessionExpired(), "Session expired or invalid, please sign in again."},
	{"dev.auth.token_failed", i18n.DevAuthTokenFailed(), "error issuing session token"},
	{"dev.auth.too_many_attempts", i18n.DevAuthTooManyAttempts(37), fmt.Sprintf("Too many attempts. Try again in %d seconds.", 37)},

	{"dev.social.bad_domain", i18n.DevSocialBadDomain("anna.example", "off-the.cloud"), fmt.Sprintf("domain %q is not a %s address", "anna.example", "off-the.cloud")},
	{"dev.social.blocked", i18n.DevSocialBlocked(), "you blocked this device - unblock it before sending a request"},
	{"dev.social.change_status_failed", i18n.DevSocialChangeStatusFailed("boom"), fmt.Sprintf("Error trying to change friend status: %s", "boom")},
	{"dev.social.comment_failed", i18n.DevSocialCommentFailed("boom"), fmt.Sprintf("error publishing comment: %s", "boom")},
	{"dev.social.comment_likers_failed", i18n.DevSocialCommentLikersFailed("boom"), fmt.Sprintf("error getting comment likers: %s", "boom")},
	{"dev.social.delete_comment_failed", i18n.DevSocialDeleteCommentFailed("boom"), fmt.Sprintf("error deleting comment: %s", "boom")},
	{"dev.social.delete_friendship_failed", i18n.DevSocialDeleteFriendshipFailed("boom"), fmt.Sprintf("Error trying to delete the friendship: %s", "boom")},
	{"dev.social.delete_post_failed", i18n.DevSocialDeletePostFailed("boom"), fmt.Sprintf("error deleting publication: %s", "boom")},
	{"dev.social.events_failed", i18n.DevSocialEventsFailed("boom"), fmt.Sprintf("error trying to retrieve events: %s", "boom")},
	{"dev.social.friendship_lookup_failed", i18n.DevSocialFriendshipLookupFailed("boom"), fmt.Sprintf("error retreiving friendship: %s", "boom")},
	{"dev.social.friendships_failed", i18n.DevSocialFriendshipsFailed("boom"), fmt.Sprintf("Error reading friendships: %s", "boom")},
	{"dev.social.like_comment_failed", i18n.DevSocialLikeCommentFailed("boom"), fmt.Sprintf("error liking publication comment: %s", "boom")},
	{"dev.social.like_post_failed", i18n.DevSocialLikePostFailed("boom"), fmt.Sprintf("error liking publication: %s", "boom")},
	{"dev.social.load_file_failed", i18n.DevSocialLoadFileFailed("/Trip/a.jpg", "boom"), fmt.Sprintf("error loading file %q: %v", "/Trip/a.jpg", "boom")},
	{"dev.social.media_not_available", i18n.DevSocialMediaNotAvailable(), "media not available"},
	// New with the catalog (a post's file too large to send whole), so no
	// older English to keep.
	{"dev.social.media_too_large", i18n.DevSocialMediaTooLarge(600_000_000), "this file is too large to send whole (600 MB)"},
	{"dev.social.no_friendship_with", i18n.DevSocialNoFriendshipWith("anna.off-the.cloud"), fmt.Sprintf("no friendship with %s", "anna.off-the.cloud")},
	{"dev.social.no_longer_friends", i18n.DevSocialNoLongerFriends(), "no longer friends"},
	{"dev.social.no_such_comment", i18n.DevSocialNoSuchComment(), "no such comment"},
	{"dev.social.no_such_post", i18n.DevSocialNoSuchPost(), "no such publication"},
	{"dev.social.not_accepted", i18n.DevSocialNotAccepted(), "Friendship not accepted"},
	{"dev.social.not_found", i18n.DevSocialNotFound(), "Friendship not found"},
	{"dev.social.not_found_error", i18n.DevSocialNotFoundError(), "Error: Friendship not found"},
	{"dev.social.not_your_comment", i18n.DevSocialNotYourComment(), "not your comment, nor a comment on one of your posts"},
	{"dev.social.not_your_post", i18n.DevSocialNotYourPost(), "not your publication"},
	{"dev.social.post_failed", i18n.DevSocialPostFailed("boom"), fmt.Sprintf("error trying to create publication: %s", "boom")},
	{"dev.social.post_likers_failed", i18n.DevSocialPostLikersFailed("boom"), fmt.Sprintf("error getting publication likers: %s", "boom")},
	{"dev.social.posts_failed", i18n.DevSocialPostsFailed("boom"), fmt.Sprintf("error trying to collect publications: %s", "boom")},
	{"dev.social.request_failed", i18n.DevSocialRequestFailed("boom"), fmt.Sprintf("Could not send the request: %s", "boom")},
	{"dev.social.request_refused", i18n.DevSocialRequestRefused(), "friendship request refused"},
	{"dev.social.request_refused_error", i18n.DevSocialRequestRefusedError(), "Error: friendship request refused"},
	{"dev.social.video_thumbnail_failed", i18n.DevSocialVideoThumbnailFailed("/v.mov", "boom"), fmt.Sprintf("generating thumbnail for compressed video %q: %v", "/v.mov", "boom")},

	{"dev.files.archive_offset", i18n.DevFilesArchiveOffset(512), fmt.Sprintf("offset %d outside the archive", 512)},
	{"dev.files.archive_too_big", i18n.DevFilesArchiveTooBig(999), fmt.Sprintf("the archive is %d bytes: download it in parts", 999)},
	{"dev.files.asset_not_found", i18n.DevFilesAssetNotFound(), "asset not found"},
	{"dev.files.bad_size", i18n.DevFilesBadSize(), "bad size"},
	{"dev.files.chunk_too_big", i18n.DevFilesChunkTooBig(400), fmt.Sprintf("chunk over %d bytes", 400)},
	{"dev.files.cloud_ids_failed", i18n.DevFilesCloudIdsFailed("boom"), fmt.Sprintf("error checking cloud ids: %s", "boom")},
	{"dev.files.collection_needs_name", i18n.DevFilesCollectionNeedsName(), "a collection needs a name"},
	// Issue #201: new with the catalog, so no older English to keep.
	{"dev.files.content_changed", i18n.DevFilesContentChanged(), "this file has changed since it was shown, so nothing was deleted: refresh and try again"},
	{"dev.files.content_missing", i18n.DevFilesContentMissing("/Trip/a.jpg"), fmt.Sprintf("the content of %s is missing on this device", "/Trip/a.jpg")},
	{"dev.files.content_unavailable", i18n.DevFilesContentUnavailable("/Trip/a.jpg"), fmt.Sprintf("the content of %s is missing or unreadable on this device", "/Trip/a.jpg")},
	{"dev.files.content_unreadable", i18n.DevFilesContentUnreadable("/Trip/a.jpg"), fmt.Sprintf("the content of %s is unreadable on this device", "/Trip/a.jpg")},
	// Issue #201: new with the catalog, so no older English to keep.
	{"dev.files.copy_upload_only", i18n.DevFilesCopyUploadOnly("/Backups/Phone"), fmt.Sprintf("a copy is in the upload-only folder %s: no copy was deleted", "/Backups/Phone")},
	{"dev.files.delete_failed", i18n.DevFilesDeleteFailed("boom"), fmt.Sprintf("error trying to delete file: %s", "boom")},
	{"dev.files.download_failed", i18n.DevFilesDownloadFailed("boom"), fmt.Sprintf("error trying to download file: %s", "boom")},
	{"dev.files.exif_unsupported", i18n.DevFilesExifUnsupported("application/pdf"), fmt.Sprintf("unsupported mime type for EXIF: %s", "application/pdf")},
	{"dev.files.file_not_found", i18n.DevFilesFileNotFound(), "file not found"},
	{"dev.files.file_offset", i18n.DevFilesFileOffset(77), fmt.Sprintf("offset %d outside the file", 77)},
	{"dev.files.folder_update_failed", i18n.DevFilesFolderUpdateFailed("boom"), fmt.Sprintf("error updating the folder: %s", "boom")},
	{"dev.files.gallery_not_found", i18n.DevFilesGalleryNotFound(), "this link doesn't exist or has expired"},
	{"dev.files.hash_check_failed", i18n.DevFilesHashCheckFailed("boom"), fmt.Sprintf("error checking file hash: %s", "boom")},
	{"dev.files.hash_content_missing", i18n.DevFilesHashContentMissing(), "the content for that hash is missing on this device: upload it instead"},
	{"dev.files.image_grid_mismatch", i18n.DevFilesImageGridMismatch(900, 800, 512, 512), fmt.Sprintf("the image says it is %dx%d, larger than its %dx%d grid", 900, 800, 512, 512)},
	{"dev.files.image_too_large", i18n.DevFilesImageTooLarge(900, 800, 120), fmt.Sprintf("the image is %dx%d, %s (over %d megapixels)", 900, 800, "too large to process", 120)},
	{"dev.files.info_failed", i18n.DevFilesInfoFailed("boom"), fmt.Sprintf("error trying to retrieve file info: %s", "boom")},
	{"dev.files.link_expired", i18n.DevFilesLinkExpired(), "shared link has expired"},
	{"dev.files.link_failed", i18n.DevFilesLinkFailed("boom"), fmt.Sprintf("error trying to link file: %s", "boom")},
	{"dev.files.locked_by_parent", i18n.DevFilesLockedByParent("Trip", "/Photos"), fmt.Sprintf("%s is inside the upload-only folder %s - unlock %s to unlock it", "Trip", "/Photos", "/Photos")},
	{"dev.files.locked_by_parent_own", i18n.DevFilesLockedByParentOwn("Trip", "/Photos"), fmt.Sprintf("%s is inside the upload-only folder %s - unlock %s first, then %s", "Trip", "/Photos", "/Photos", "Trip")},
	{"dev.files.media_not_found", i18n.DevFilesMediaNotFound(), "media not found"},
	{"dev.files.no_low_res_photos", i18n.DevFilesNoLowResPhotos(), "there are no photos to share in low resolution there"},
	{"dev.files.no_media_to_share", i18n.DevFilesNoMediaToShare(), "there are no photos or videos to share there"},
	{"dev.files.no_path", i18n.DevFilesNoPath(), "no path"},
	{"dev.files.no_such_hash", i18n.DevFilesNoSuchHash(), "no file with that hash on this device"},
	{"dev.files.no_such_job", i18n.DevFilesNoSuchJob(), "no such job"},
	{"dev.files.no_such_link", i18n.DevFilesNoSuchLink(), "no such link"},
	{"dev.files.no_such_path", i18n.DevFilesNoSuchPath("/Trip"), fmt.Sprintf("no such file or directory: %s", "/Trip")},
	{"dev.files.not_on_device", i18n.DevFilesNotOnDevice("/Trip/a.jpg"), fmt.Sprintf("%s is not on this device", "/Trip/a.jpg")},
	{"dev.files.nothing_to_share", i18n.DevFilesNothingToShare(), "nothing to share"},
	{"dev.files.nothing_to_stream", i18n.DevFilesNothingToStream(), "nothing to stream"},
	{"dev.files.out_of_images_list_failed", i18n.DevFilesOutOfImagesListFailed("boom"), fmt.Sprintf("error listing the folders kept out of Images: %s", "boom")},
	// "Not this person" / "Someone else…": new, so their English is the
	// catalog's from the start.
	{"dev.files.person_gone", i18n.DevFilesPersonGone(), "that person is no longer on this device"},
	{"dev.files.person_missing", i18n.DevFilesPersonMissing(), "no person given"},
	{"dev.files.person_no_photos", i18n.DevFilesPersonNoPhotos(), "no photos given"},
	{"dev.files.person_same", i18n.DevFilesPersonSame(), "choose another person to move the photos to"},
	{"dev.files.person_too_many", i18n.DevFilesPersonTooMany(cMaxRemoveFromPerson), "at most 10,000 photos at a time"},
	{"dev.files.post_content_unavailable", i18n.DevFilesPostContentUnavailable("ab12", "boom"), fmt.Sprintf("the content of %s is missing or unreadable: %v", "ab12", "boom")},
	{"dev.files.read_failed", i18n.DevFilesReadFailed("boom"), fmt.Sprintf("error reading file: %s", "boom")},
	{"dev.files.reprocess_count_failed", i18n.DevFilesReprocessCountFailed("boom"), fmt.Sprintf("counting media for reprocess: %v", "boom")},
	{"dev.files.reprocess_read_failed", i18n.DevFilesReprocessReadFailed("boom"), fmt.Sprintf("reading reprocess state: %v", "boom")},
	{"dev.files.reprocess_resume_failed", i18n.DevFilesReprocessResumeFailed("boom"), fmt.Sprintf("resuming reprocess: %v", "boom")},
	{"dev.files.reprocess_start_failed", i18n.DevFilesReprocessStartFailed("boom"), fmt.Sprintf("starting reprocess: %v", "boom")},
	{"dev.files.reprocess_wipe_failed", i18n.DevFilesReprocessWipeFailed("boom"), fmt.Sprintf("wiping tags/faces before reprocess: %v", "boom")},
	{"dev.files.retrieve_failed", i18n.DevFilesRetrieveFailed("boom"), fmt.Sprintf("error trying to retrieve file: %s", "boom")},
	{"dev.files.share_link_failed", i18n.DevFilesShareLinkFailed("boom"), fmt.Sprintf("error creating files share link: %s", "boom")},
	{"dev.files.storage_full", i18n.DevFilesStorageFull(3_400_000_000, 1_200_000_000), "not enough space on the device: this needs 3.4 GB, and only 1.2 GB is left"},
	{"dev.files.stream_failed", i18n.DevFilesStreamFailed(), "could not prepare the stream"},
	{"dev.files.too_large_to_open", i18n.DevFilesTooLargeToOpen(1_500_000_000), "this file is too large to open here (1.5 GB): download it instead"},
	{"dev.files.upload_bad_hash", i18n.DevFilesUploadBadHash(), "the upload's content doesn't match its hash"},
	{"dev.files.upload_failed", i18n.DevFilesUploadFailed("boom"), fmt.Sprintf("error trying to upload file: %s", "boom")},
	{"dev.files.upload_finish_failed", i18n.DevFilesUploadFinishFailed("boom"), fmt.Sprintf("error finishing the upload: %s", "boom")},
	{"dev.files.upload_incomplete", i18n.DevFilesUploadIncomplete(10, 900), fmt.Sprintf("upload incomplete: %d of %d bytes", 10, 900)},
	{"dev.files.places_off", i18n.DevFilesPlacesOff(), "Places is off: turn it on in Settings to see your photos on a map"},
	{"dev.files.upload_only", i18n.DevFilesUploadOnly(), "this folder is upload only: nothing in it can be deleted"},
	{"dev.files.upload_out_of_order", i18n.DevFilesUploadOutOfOrder(), "upload chunk out of order"},
	{"dev.files.upload_receive_failed", i18n.DevFilesUploadReceiveFailed("boom"), fmt.Sprintf("error receiving the upload: %s", "boom")},
	{"dev.files.upload_removed", i18n.DevFilesUploadRemoved(), "the upload's content was removed before it was recorded - send it again"},
	{"dev.files.upload_start_failed", i18n.DevFilesUploadStartFailed("boom"), fmt.Sprintf("error starting the upload: %s", "boom")},
	{"dev.files.upload_too_much", i18n.DevFilesUploadTooMuch(), "more data than the upload's size"},
	{"dev.files.upload_unknown", i18n.DevFilesUploadUnknown(), "unknown or expired upload"},
	{"dev.files.versions_failed", i18n.DevFilesVersionsFailed("boom"), fmt.Sprintf("error listing the versions: %s", "boom")},
	{"dev.files.video_no_duration", i18n.DevFilesVideoNoDuration(), "video has no usable duration"},
	// New with the catalog (a post's video that couldn't be re-encoded), so
	// no older English to keep.
	{"dev.files.video_not_cut", i18n.DevFilesVideoNotCut(), "the video could not be cut, so nothing was posted: try again, or post it without cutting it"},
	{"dev.files.video_not_reduced", i18n.DevFilesVideoNotReduced(3_200_000_000), "the video could not be made smaller for the post, and at 3.2 GB it is too large to share as it is"},
	{"dev.files.video_no_frames", i18n.DevFilesVideoNoFrames(), "could not extract any frames from video"},
	{"dev.files.video_no_frames_extracted", i18n.DevFilesVideoNoFramesExtracted(), "no frames extracted from video"},

	{"dev.users.bad_username", i18n.DevUsersBadUsername(), "username must be lowercase letters, digits, or hyphens"},
	{"dev.users.bridge_check_failed", i18n.DevUsersBridgeCheckFailed("kim.anna.off-the.cloud", "boom"), fmt.Sprintf("could not reach the bridge to check %s, so this user was not created - try again in a moment, or uncheck \"request bridge access\" to create a local-only account: %v", "kim.anna.off-the.cloud", "boom")},
	{"dev.users.confirm_mismatch", i18n.DevUsersConfirmMismatch(), "confirmation username does not match"},
	{"dev.users.bridge_domain_limit", i18n.DevUsersBridgeDomainLimit("pepe.off-the.cloud"), "pepe.off-the.cloud can't be added: your account on the bridge already holds as many addresses as it may - remove one on its account page, or uncheck \"request bridge access\" to create this user for your own network only"},
	{"dev.users.name_taken_on_bridge", i18n.DevUsersNameTakenOnBridge("kim.anna.off-the.cloud"), fmt.Sprintf("%s is already taken on the bridge - pick a different username, or uncheck \"request bridge access\" to create this user for your own network only", "kim.anna.off-the.cloud")},
	{"dev.users.port_in_use", i18n.DevUsersPortInUse("8082"), fmt.Sprintf("port %d is already in use by another user", 8082)},
	{"dev.users.stop_failed", i18n.DevUsersStopFailed("kim", "boom"), fmt.Sprintf("could not stop %s's process: %v", "kim", "boom")},
	{"dev.users.storage_dir_failed", i18n.DevUsersStorageDirFailed("boom"), fmt.Sprintf("creating storage directory: %v", "boom")},
	{"dev.users.username_taken", i18n.DevUsersUsernameTaken(), "username already taken"},

	{"dev.device.already_on_bridge", i18n.DevDeviceAlreadyOnBridge(), "this device is already on the bridge"},
	{"dev.device.bridge_dial_failed", i18n.DevDeviceBridgeDialFailed("boom"), fmt.Sprintf("dialing bridge: %v", "boom")},
	{"dev.device.bridge_read_failed", i18n.DevDeviceBridgeReadFailed("boom"), fmt.Sprintf("reading from bridge: %v", "boom")},
	{"dev.device.bridge_secret_managed", i18n.DevDeviceBridgeSecretManaged(), "the bridge secret is managed by the device"},
	{"dev.device.bridge_write_failed", i18n.DevDeviceBridgeWriteFailed("boom"), fmt.Sprintf("writing to bridge: %v", "boom")},
	{"dev.device.identity_store_failed", i18n.DevDeviceIdentityStoreFailed(), "could not store the device's new identity"},
	{"dev.device.local_addresses_failed", i18n.DevDeviceLocalAddressesFailed(), "could not list this device's addresses"},
	{"dev.device.local_no_address", i18n.DevDeviceLocalNoAddress(), "this device has no home-network address"},
	{"dev.device.local_no_listener", i18n.DevDeviceLocalNoListener(), "this device has no home-network listener"},
	{"dev.device.local_starting", i18n.DevDeviceLocalStarting(), "this device is still starting its home-network listener"},
	{"dev.device.logs_console_only", i18n.DevDeviceLogsConsoleOnly(), "this device logs to its console only"},
	{"dev.device.logs_need_bridge", i18n.DevDeviceLogsNeedBridge(), "this device isn't on the bridge, so it can't send its logs - use Copy instead"},
	{"dev.device.not_on_bridge", i18n.DevDeviceNotOnBridge(), "this device is not on the bridge"},
	{"dev.device.release_failed", i18n.DevDeviceReleaseFailed(), "The bridge could not be reached, so the name is still registered: release it on the account page."},
	{"dev.device.restart_needs_update", i18n.DevDeviceRestartNeedsUpdate(), "this device can't be restarted from here until it is updated"},
	{"dev.device.restart_request_failed", i18n.DevDeviceRestartRequestFailed("boom"), fmt.Sprintf("could not ask for the restart: %v", "boom")},
	{"dev.device.restart_too_soon", i18n.DevDeviceRestartTooSoon(), "the device has only just started; try again in a minute"},
	{"dev.device.social_limit_too_low", i18n.DevDeviceSocialLimitTooLow(), "the limit must be at least 0.1 GB"},
	{"dev.device.storage_too_many", i18n.DevDeviceStorageTooMany(3), fmt.Sprintf("choose at most 2 devices, got %d", 3)},
	{"dev.device.unknown_log", i18n.DevDeviceUnknownLog("bogus"), fmt.Sprintf("unknown log %q", "bogus")},
	// Settings > Wi-Fi (release 140): new, so their English is the
	// catalog's from the start.
	{"dev.device.wifi_not_managed", i18n.DevDeviceWifiNotManaged(), "This device's Wi-Fi can't be changed from Settings."},
	{"dev.device.wifi_bad_name", i18n.DevDeviceWifiBadName(), "A Wi-Fi network name has 1 to 32 characters (accented letters and emoji count as two or more), without tabs or line breaks."},
	{"dev.device.wifi_bad_password", i18n.DevDeviceWifiBadPassword(), "A Wi-Fi password has 8 to 63 characters (accented letters and some symbols count as two or three), or exactly 64 hexadecimal digits."},
	// Settings › Storage's Use it for the mirror: new, so their English is
	// the catalog's from the start.
	{"dev.device.mirror_not_candidate", i18n.DevDeviceMirrorNotCandidate(), "That disk is no longer offered for the mirror: it was unplugged, or it was added already."},
	{"dev.device.mirror_not_degraded", i18n.DevDeviceMirrorNotDegraded(), "The mirror has both of its disks, so it doesn't need this one."},
	{"dev.device.mirror_busy", i18n.DevDeviceMirrorBusy(), "A disk was chosen for the mirror a moment ago. Wait a few seconds and look again."},
}

// The catalog's English is the device's English as it was.
func TestReplyKeysKeepTheirEnglish(t *testing.T) {
	for _, c := range headEnglish {
		if c.got.Key != c.key {
			t.Errorf("%s: the table pairs it with %s", c.key, c.got.Key)
		}
		for _, lang := range []string{"", "en"} {
			if got := plain(c.got.Render(lang)); got != c.want {
				t.Errorf("%s in %q:\n got %q\nwant %q", c.key, lang, got, c.want)
			}
		}
	}
}

// Every key of the reply files is in headEnglish.
func TestEveryReplyKeyIsChecked(t *testing.T) {
	checked := map[string]bool{}
	for _, c := range headEnglish {
		checked[c.key] = true
	}
	var missing []string
	for _, prefix := range []string{"dev.err", "dev.auth", "dev.files", "dev.social", "dev.users", "dev.device"} {
		data, err := os.ReadFile(filepath.Join("..", "i18n", "strings", "en", prefix+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatal(err)
		}
		for k := range entries {
			if !checked[k] {
				missing = append(missing, k)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("keys with no English check: %v", missing)
	}
}

// The replies whose catalog rendering would differ from today's English -
// a number past 999 (grouped), a user-controlled value (isolated) - keep
// that English for a request without a language or in English.
func TestEnglishRequestsKeepUngroupedNumbersAndUnmarkedNames(t *testing.T) {
	for _, lang := range []string{"", "en"} {
		if got, want := tooManyAttemptsAck(1234, lang).ErrorMsg, "Too many attempts. Try again in 1234 seconds."; got != want {
			t.Errorf("lockout in %q: got %q, want %q", lang, got, want)
		}
		if got, want := sayOr(lang, "plain", i18n.DevFilesNoSuchJob()), "plain"; got != want {
			t.Errorf("sayOr in %q: got %q", lang, got)
		}
		err := errors.New("Error 1054: Unknown column")
		if got := errText(lang, err); got != err.Error() {
			t.Errorf("errText in %q: got %q, want the error as it is", lang, got)
		}
		if got := errText(lang, filesmanager.ErrUploadOnly); got != "this folder is upload only: nothing in it can be deleted" {
			t.Errorf("errText of a translated error in %q: got %q", lang, got)
		}
	}
}

// replyCase is a request a connection answers without a database, with
// the English reply it always got.
type replyCase struct {
	name  string
	ch    func() *connHandler
	env   func() *pb.ReqEnvelope
	reply func(*pb.RespEnvelope) string
	want  string
	// in is the same text in another language.
	in func(lang string) string
}

func errorMessage(r *pb.RespEnvelope) string { return r.GetErrorMessage() }
func ackMessage(r *pb.RespEnvelope) string   { return r.GetRespAck().GetErrorMsg() }

func replyCases(t *testing.T) []replyCase {
	owner := func() *connHandler {
		ch := &connHandler{mg: &Manager{}}
		ch.setSession(newTestAuthenticatedSession(t))
		return ch
	}
	primary := func() *connHandler {
		ch := &connHandler{mg: &Manager{sup: &supervisor.Supervisor{}}}
		ch.setSession(newTestAuthenticatedSession(t))
		return ch
	}
	anonymous := func() *connHandler { return &connHandler{mg: &Manager{}} }
	return []replyCase{
		{"bridge secret", owner, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqSetBridgeSecret{ReqSetBridgeSecret: &pb.SetBridgeSecret{}}}
		}, errorMessage, "the bridge secret is managed by the device", i18n.DevDeviceBridgeSecretManaged().Render},
		{"unknown job", owner, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqGetSharedGalleryJob{ReqGetSharedGalleryJob: &pb.GetSharedGalleryJob{JobId: "nope"}}}
		}, errorMessage, "no such job", i18n.DevFilesNoSuchJob().Render},
		{"collection without a name", owner, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqCreateImageGroup{ReqCreateImageGroup: &pb.CreateImageGroup{Name: "  "}}}
		}, errorMessage, "a collection needs a name", i18n.DevFilesCollectionNeedsName().Render},
		{"friends' posts limit", owner, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqSetSocialStorageLimit{ReqSetSocialStorageLimit: &pb.SetSocialStorageLimit{Mb: 5}}}
		}, errorMessage, "the limit must be at least 0.1 GB", i18n.DevDeviceSocialLimitTooLow().Render},
		{"a newer app's request", owner, func() *pb.ReqEnvelope { return &pb.ReqEnvelope{} }, errorMessage,
			"This device does not understand that request: its software is older than the app. Check for updates in Settings.", i18n.DevErrUnknownRequest().Render},
		{"child instance", owner, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqListUsers{ReqListUsers: &pb.ReqListUsers{}}}
		}, errorMessage, "not available on this instance", i18n.DevErrNotOnThisInstance().Render},
		{"unknown log", primary, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqGetLogs{ReqGetLogs: &pb.GetLogs{Source: "bogus\u202e"}}}
		}, errorMessage, "unknown log \"bogus\\u202e\"", i18n.DevDeviceUnknownLog(`bogus\u202e`).Render},
		{"logs without the bridge", primary, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqSendLogs{ReqSendLogs: &pb.SendLogs{}}}
		}, errorMessage, "this device isn't on the bridge, so it can't send its logs - use Copy instead", i18n.DevDeviceLogsNeedBridge().Render},
		{"too many disks", primary, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqSetupStorage{ReqSetupStorage: &pb.SetupStorage{DevicePaths: make([]string, 1200)}}}
		}, errorMessage, "choose at most 2 devices, got 1200", i18n.DevDeviceStorageTooMany(1200).Render},
		{"Wi-Fi without a name", primary, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqJoinWifi{ReqJoinWifi: &pb.JoinWifi{}}}
		}, errorMessage, "A Wi-Fi network name has 1 to 32 characters (accented letters and emoji count as two or more), without tabs or line breaks.", i18n.DevDeviceWifiBadName().Render},
		{"home network not known yet", owner, localEndpointReq, errorMessage,
			"this device is still starting its home-network listener", i18n.DevDeviceLocalStarting().Render},
		{"password change without a key", owner, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqChangeKey{ReqChangeKey: &pb.ChangeKey{}}}
		}, errorMessage, "no public key was requested for this connection", i18n.DevAuthNoPublicKey().Render},
		{"expired saved sign-in", anonymous, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqAuthWithToken{ReqAuthWithToken: &pb.ReqAuthWithToken{Token: "nope"}}}
		}, ackMessage, "Session expired or invalid, please sign in again.", i18n.DevAuthSessionExpired().Render},
		{"no session", anonymous, func() *pb.ReqEnvelope {
			return &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqGetFile{ReqGetFile: &pb.GetFile{}}}
		}, ackMessage, "not authenticated", i18n.DevErrNotAuthenticated().Render},
	}
}

// answer runs one request as a connection does, nil reply included.
func answer(ch *connHandler, env *pb.ReqEnvelope) *pb.RespEnvelope {
	resp, _ := ch.processMessage(env)
	if resp == nil {
		resp = notAuthenticatedResponse(env.Id, requestLang(env))
	}
	return resp
}

// A request without a language, and one in English or in a language this
// build has no text for, gets the reply it always got.
func TestRepliesWithoutALanguageAreTodaysEnglish(t *testing.T) {
	for _, c := range replyCases(t) {
		for _, lang := range []string{"", "en", "xx", "en-GB"} {
			env := c.env()
			env.Lang = lang
			if got := c.reply(answer(c.ch(), env)); got != c.want {
				t.Errorf("%s in %q:\n got %q\nwant %q", c.name, lang, got, c.want)
			}
		}
	}
}

// otherLanguage is a language this build carries text in besides English:
// the pseudo-locale in a `make i18n DRAFT=1` copy. Shipping builds carry
// English only until a translation ships, and skip.
func otherLanguage(t *testing.T) string {
	var found string
	for _, l := range i18n.Languages() {
		if l.Code == "en" || i18n.DevFilesNoSuchJob().Render(l.Code) == "no such job" {
			continue
		}
		if found == "" || l.Code == "qps" {
			found = l.Code
		}
	}
	if found == "" {
		t.Skip("no language but English has text in this build: run in a `make i18n DRAFT=1` copy")
	}
	return found
}

// Replies follow the request's language.
func TestRepliesFollowTheRequestsLanguage(t *testing.T) {
	lang := otherLanguage(t)
	for _, c := range replyCases(t) {
		env := c.env()
		env.Lang = lang
		got, want := c.reply(answer(c.ch(), env)), c.in(lang)
		if got != want || got == c.want {
			t.Errorf("%s in %q:\n got %q\nwant %q (and not the English)", c.name, lang, got, want)
		}
	}
	// An error the device can't translate comes after a sentence in the
	// request's language; one of its own is in that language whole.
	raw := errors.New("Error 1054: Unknown column")
	if got, want := errText(lang, raw), i18n.DevErrActionFailed(raw.Error()).Render(lang); got != want || !strings.Contains(got, raw.Error()) {
		t.Errorf("errText of a raw error: got %q, want %q", got, want)
	}
	if got, want := devtext.Text(lang, filesmanager.ErrUploadOnly), i18n.DevFilesUploadOnly().Render(lang); got != want {
		t.Errorf("a files_manager error: got %q, want %q", got, want)
	}
	locked := &filesmanager.LockedByParentError{Folder: "/Photos/Trip/", Parent: "/Photos/", OwnFlag: true}
	if got, want := devtext.Text(lang, locked), i18n.DevFilesLockedByParentOwn("Trip", "/Photos").Render(lang); got != want {
		t.Errorf("locked_by_parent: got %q, want %q", got, want)
	}
	if got := tooManyAttemptsAck(1234, lang).ErrorMsg; got != i18n.DevAuthTooManyAttempts(1234).Render(lang) {
		t.Errorf("lockout: got %q", got)
	}
}

// In the pseudo-locale every reply key has its own text: none is left in
// English (a key the Go catalog lacks would fall back to it).
func TestEveryReplyKeyRendersInThePseudoLocale(t *testing.T) {
	if otherLanguage(t) != "qps" {
		t.Skip("the pseudo-locale is only in a `make i18n DRAFT=1` copy")
	}
	for _, c := range headEnglish {
		if got := plain(c.got.Render("qps")); got == c.want || !strings.HasPrefix(got, "[") {
			t.Errorf("%s in qps: %q", c.key, got)
		}
	}
}
