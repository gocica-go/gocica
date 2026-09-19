// Deletes every Actions cache entry whose key starts with one of `prefixes`.
//
// The repository shares a single cache quota, so a benchmark that leaves old
// entries behind measures eviction as much as it measures gocica.
module.exports = async ({ github, context, prefixes }) => {
  for (const prefix of prefixes) {
    let deleted = 0

    for (let page = 1; ; page++) {
      const { data } = await github.rest.actions.getActionsCacheList({
        owner: context.repo.owner,
        repo: context.repo.repo,
        key: prefix,
        per_page: 100,
        page,
      })

      if (data.actions_caches.length === 0) {
        break
      }

      for (const cache of data.actions_caches) {
        await github.rest.actions.deleteActionsCacheById({
          owner: context.repo.owner,
          repo: context.repo.repo,
          cache_id: cache.id,
        })
        deleted++
      }

      // Deleting shifts the pages under us, so always re-read the first one.
      page = 0
    }

    console.log(`deleted ${deleted} cache entries with prefix ${prefix}`)
  }
}
