#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The demo device's friends (docs/demo-device.md): three made-up friends,
# their posts, and their comments and likes on the family's posts, written
# straight into the device's database as a synced friend's would be. Their
# addresses are under demo.off-the.cloud (marta.demo.off-the.cloud...):
# bridge names have no dots, so nobody can ever register one, and they don't
# resolve - the device's friend sync fails quietly and keeps what is here.
#
#   python3 friends.py OUT   (OUT = ~/src/otc-demo-content)
#
# Reads OUT/friends/raw/*.png (generated with docs/promo/tools/gen.py from
# OUT/friends/prompts) and posts.json; writes OUT/friends/out/: the post
# media named by hash with their _thumbnail, and seed.sql.
import hashlib, json, os, subprocess, sys, uuid

out = sys.argv[1]
raw = os.path.join(out, "friends", "raw")
dest = os.path.join(out, "friends", "out")
media = os.path.join(dest, "media")
os.makedirs(media, exist_ok=True)

def jpeg(src, long_side, path, square=False):
    vf = (f"scale={long_side}:{long_side}:force_original_aspect_ratio=increase,crop={long_side}:{long_side}" if square
          else f"scale='if(gt(iw,ih),{long_side},-2)':'if(gt(iw,ih),-2,{long_side})'")
    subprocess.run(["ffmpeg", "-loglevel", "error", "-y", "-i", src, "-vf", vf, "-q:v", "3", path], check=True)

def sql_str(s):
    return "'" + s.replace("\\", "\\\\").replace("'", "''") + "'"

FRIENDS = {
    "marta": ("marta.demo.off-the.cloud", "Marta", "Lucía's sister. Valencia, paella and now a puppy.", "marta_portrait"),
    "rosajan": ("rosaandjan.demo.off-the.cloud", "Rosa & Jan", "Grandma and Grandpa: tomatoes, cakes and the grandchildren.", "rosajan_greenhouse"),
    "okafor": ("okafor.demo.off-the.cloud", "The Okafors", "Ada, Kunle and Zoe. Always planning the next walk.", "okafor_portrait"),
}
POSTS = [  # friend, date, caption, images
    ("rosajan", "2025-08-30 18:20:00", "This year's tomatoes. Jan says it's the sun; I say it's my compost.", ["rosajan_greenhouse"]),
    ("marta", "2025-07-20 15:05:00", "Paella Sunday on the beach. Whoever finishes first does the dishes.", ["marta_paella"]),
    ("okafor", "2025-10-12 13:40:00", "Cornwall in October: wind, sun and a very long cliff path.", ["okafor_hike"]),
    ("rosajan", "2026-04-12 16:30:00", "Jan's apple cake, version 12. The best one yet, apparently.", ["rosajan_cake"]),
    ("marta", "2026-05-02 19:10:00", "Meet Churro. He has already eaten one shoe.", ["marta_puppy"]),
    ("okafor", "2026-06-28 20:15:00", "Picnic by the lake until the mosquitoes won.", ["okafor_picnic"]),
]
# Comments on the family's own posts, by their caption's first words.
OWN_POST_DATES = {
    "Two weeks of sun": "2025-08-21 20:30:00",
    "Christmas at Rosa": "2025-12-27 11:15:00",
    "First ski week": "2026-02-21 19:00:00",
    "Leo is five": "2026-03-14 21:00:00",
}
OWN_COMMENTS = {
    "Two weeks of sun": [("marta", "Leo the beach bum! Those caves look unreal.", 2), ("rosajan", "Beautiful. Come on Sunday and tell us everything.", 5), ("okafor", "Adding the Algarve to our list for next summer.", 26)],
    "Christmas at Rosa": [("rosajan", "Best Christmas in years. The lasagna was gone in ten minutes.", 1), ("marta", "I missed you all! Save me some cookies next year.", 3)],
    "First ski week": [("okafor", "Nora on the red runs already? Impressive.", 1), ("rosajan", "Be careful on those slopes, all of you.", 2)],
    "Leo is five": [("marta", "Happy birthday, Leo! A dinosaur cake is the right call.", 1), ("rosajan", "Our big boy. Five already.", 1), ("okafor", "Happy birthday from Ada, Kunle and Zoe!", 4)],
}
OWN_LIKES = {"Two weeks of sun": ["marta", "rosajan", "okafor"], "Christmas at Rosa": ["marta", "rosajan", "okafor"],
             "First ski week": ["rosajan", "okafor"], "Leo is five": ["marta", "rosajan", "okafor"]}
# Comments on friends' posts by the other friends.
FRIEND_COMMENTS = {
    0: [("marta", "Send some to Valencia!", 3)],
    1: [("rosajan", "That looks delicious. Next time we're coming.", 4)],
    2: [("marta", "I'd have turned back at the first gust.", 2)],
    3: [("okafor", "Version 12 looks perfect. Recipe please?", 1)],
    4: [("rosajan", "He's adorable. Keep the shoes on the top shelf.", 1), ("okafor", "Zoe wants to meet him!", 2)],
    5: [("marta", "Mosquitoes always win in the end.", 1)],
}
FRIEND_LIKES = {0: ["marta", "okafor"], 1: ["rosajan"], 2: ["marta", "rosajan"], 3: ["marta", "okafor"], 4: ["rosajan", "okafor"], 5: ["rosajan"]}

def plus_hours(dt, h):
    import datetime
    d = datetime.datetime.strptime(dt, "%Y-%m-%d %H:%M:%S") + datetime.timedelta(hours=h)
    return d.strftime("%Y-%m-%d %H:%M:%S")

sql = ["-- docs/demo-device/content/friends.py: the demo device's friends", "start transaction;"]
for key, (domain, name, text, avatar) in FRIENDS.items():
    p = os.path.join(dest, f"avatar-{key}.jpg")
    jpeg(os.path.join(raw, avatar + "-1.png"), 400, p, square=True)
    img = open(p, "rb").read().hex()
    sql.append(f"delete from social_friendship where domain = {sql_str(domain)};")
    sql.append("insert into social_friendship (domain, status, name, image, text, secret, sent, latest_sync, notifications_started, last_seen) values "
               f"({sql_str(domain)}, 'accepted', {sql_str(name)}, X'{img}', {sql_str(text)}, {sql_str(uuid.uuid4().hex)}, 0, now(), 1, now());")

files = []
for i, (key, dt, caption, images) in enumerate(POSTS):
    domain = FRIENDS[key][0]
    pub = str(uuid.uuid5(uuid.NAMESPACE_URL, f"otc-demo-post-{i}"))
    sql.append(f"delete from social_publications_files where uuid = {sql_str(pub)};")
    sql.append(f"delete from social_publications_comments where pub_uuid = {sql_str(pub)};")
    sql.append(f"delete from social_publication_likes where pub_uuid = {sql_str(pub)};")
    sql.append(f"delete from social_publications where uuid = {sql_str(pub)};")
    likes = FRIEND_LIKES.get(i, [])
    sql.append(f"insert into social_publications (uuid, dt, text, likes, friend_domain, own_publication) values ({sql_str(pub)}, {sql_str(dt)}, {sql_str(caption)}, {len(likes)}, {sql_str(domain)}, 0);")
    for pos, im in enumerate(images):
        full = os.path.join(dest, f"{im}.jpg")
        jpeg(os.path.join(raw, im + "-1.png"), 2048, full)
        h = hashlib.sha256(open(full, "rb").read()).hexdigest()
        os.replace(full, os.path.join(media, h))
        jpeg(os.path.join(raw, im + "-1.png"), 1000, os.path.join(media, h + "_thumbnail.jpg"))
        os.replace(os.path.join(media, h + "_thumbnail.jpg"), os.path.join(media, h + "_thumbnail"))
        size = os.path.getsize(os.path.join(media, h))
        files.append(h)
        sql.append(f"insert into social_publications_files (pos, hash, uuid, mime, created, modified, size) values ({pos}, '{h}', {sql_str(pub)}, 'image/jpeg', {sql_str(dt)}, {sql_str(dt)}, {size});")
    for j, (who, comment, h) in enumerate(FRIEND_COMMENTS.get(i, [])):
        cid = str(uuid.uuid5(uuid.NAMESPACE_URL, f"otc-demo-post-{i}-comment-{j}"))
        sql.append("insert into social_publications_comments (uuid, pub_uuid, dt, comment, publisher_name, likes, own_comment, author_domain) values "
                   f"({sql_str(cid)}, {sql_str(pub)}, {sql_str(plus_hours(dt, h))}, {sql_str(comment)}, {sql_str(FRIENDS[who][1])}, 0, 0, {sql_str(FRIENDS[who][0])});")
    for j, who in enumerate(likes):
        lid = str(uuid.uuid5(uuid.NAMESPACE_URL, f"otc-demo-post-{i}-like-{j}"))
        sql.append(f"insert into social_publication_likes (uuid, pub_uuid, dt, friend_domain) values ({sql_str(lid)}, {sql_str(pub)}, {sql_str(plus_hours(dt, 1 + j))}, {sql_str(FRIENDS[who][0])});")

# The family's own posts: their real dates, and the friends' comments and likes.
for prefix, dt in OWN_POST_DATES.items():
    where = f"own_publication = 1 and text like {sql_str(prefix + '%')}"
    sql.append(f"set @pub = (select uuid from social_publications where {where} limit 1);")
    sql.append(f"update social_publications set dt = {sql_str(dt)} where uuid = @pub;")
    sql.append("delete from social_publications_comments where pub_uuid = @pub and own_comment = 0;")
    sql.append("delete from social_publication_likes where pub_uuid = @pub;")
    sql.append("delete from notifications where pub_uuid = @pub;")
    for j, (who, comment, h) in enumerate(OWN_COMMENTS[prefix]):
        cid = str(uuid.uuid5(uuid.NAMESPACE_URL, f"otc-demo-own-{prefix}-comment-{j}"))
        when = plus_hours(dt, h)
        sql.append("insert into social_publications_comments (uuid, pub_uuid, dt, comment, publisher_name, likes, own_comment, author_domain) values "
                   f"({sql_str(cid)}, @pub, {sql_str(when)}, {sql_str(comment)}, {sql_str(FRIENDS[who][1])}, 0, 0, {sql_str(FRIENDS[who][0])});")
        sql.append("insert into notifications (uuid, dt, type, actor_name, actor_domain, pub_uuid, comment_uuid, acknowledged) values "
                   f"({sql_str(str(uuid.uuid4()))}, {sql_str(when)}, 'NewComment', {sql_str(FRIENDS[who][1])}, {sql_str(FRIENDS[who][0])}, @pub, {sql_str(cid)}, {1 if prefix != 'Leo is five' else 0});")
    for j, who in enumerate(OWN_LIKES[prefix]):
        lid = str(uuid.uuid5(uuid.NAMESPACE_URL, f"otc-demo-own-{prefix}-like-{j}"))
        when = plus_hours(dt, 1 + j)
        sql.append(f"insert into social_publication_likes (uuid, pub_uuid, dt, friend_domain) values ({sql_str(lid)}, @pub, {sql_str(when)}, {sql_str(FRIENDS[who][0])});")
        sql.append("insert into notifications (uuid, dt, type, actor_name, actor_domain, pub_uuid, acknowledged) values "
                   f"({sql_str(str(uuid.uuid4()))}, {sql_str(when)}, 'LikePublication', {sql_str(FRIENDS[who][1])}, {sql_str(FRIENDS[who][0])}, @pub, {1 if prefix != 'Leo is five' else 0});")
    sql.append(f"update social_publications set likes = {len(OWN_LIKES[prefix])} where uuid = @pub;")
sql.append("commit;")
open(os.path.join(dest, "seed.sql"), "w").write("\n".join(sql) + "\n")
print(len(files), "media files,", len(sql), "statements ->", dest)
