<script setup>
import { data as releases } from './releases.data'
</script>

# Releases

Every dufflebag release, newest first, with the notes published on its
[GitHub release](https://github.com/benemon/dufflebag/releases). Each GitHub
release also carries the image's SBOM. Read the notes for every release
between your version and the target before [upgrading](../quick-start/upgrading.md).

<div v-for="release in releases" :key="release.tag">
  <h2 :id="release.tag">{{ release.tag }}</h2>
  <p>{{ release.date }} · <a :href="release.url">GitHub release</a></p>
  <div v-html="release.notes"></div>
</div>
