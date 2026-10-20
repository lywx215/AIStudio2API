<script setup lang="ts">
import { onMounted, onUnmounted, ref, useTemplateRef } from 'vue'
const element = useTemplateRef<HTMLElement>('element')
const visible = ref(false)
let observer: IntersectionObserver | undefined
onMounted(() => {
  if (!element.value || typeof IntersectionObserver === 'undefined') {
    visible.value = true
    return
  }
  observer = new IntersectionObserver((entries) => {
    if (entries.some((entry) => entry.isIntersecting)) {
      visible.value = true
      observer?.disconnect()
    }
  })
  observer.observe(element.value)
})
onUnmounted(() => observer?.disconnect())
</script>
<template>
  <div ref="element">
    <slot v-if="visible" />
    <div v-else class="h-full min-h-56 animate-pulse rounded bg-raised/30" aria-hidden="true"></div>
  </div>
</template>
