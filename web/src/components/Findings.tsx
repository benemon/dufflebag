/**
 * The word "clean" never appears here, and neither does "stale". A registry
 * that has not examined something must not imply it is safe, and a figure that
 * has stopped being maintained is shown as an as-of fact rather than labelled
 * with a word users skim past.
 */

export const SEVERITY_COLOUR: Record<string, 'red' | 'orange' | 'yellow' | 'blue' | 'grey'> = {
  critical: 'red',
  high: 'orange',
  medium: 'yellow',
  low: 'blue',
  negligible: 'grey',
  unknown: 'grey',
}

/** The class that marks figures no longer being maintained. */
export const OUT_OF_SCAN_SET_CLASS = 'dfbg-findings-unmaintained'
