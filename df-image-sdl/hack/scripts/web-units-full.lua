-- web-units-full: full Dwarf-Therapist-style payload for /therapist.
-- Invoked via: dfhack-run web-units-full
-- Output: single JSON document, see plan in /home/user01/.claude/plans for shape.
--
-- Design notes:
--   * Every string passes through dfhack.df2utf — DF strings are CP437, the
--     proxy serves UTF-8 to the browser. Skipping df2utf was the failure
--     mode in earlier iterations.
--   * Speculative struct accesses are wrapped in pcall so a missing field on
--     a future DF/DFHack patch turns into a missing key, not a 500.
--   * Enum bounds use _last_item so the script tracks whatever DF version
--     DFHack is loaded against.
--   * Role formulas are emitted as data; the UI computes the 0-100 scores.
--     This keeps the script short and lets users tweak roles client-side
--     without re-deploying.

local json = require('json')

-- ---------------------------------------------------------------------------
-- helpers
-- ---------------------------------------------------------------------------

local function df2utf(s) return dfhack.df2utf(s or '') end

local function try(f, default)
    local ok, v = pcall(f)
    if ok then return v end
    return default
end

local function dump_enum(e)
    -- Returns a {[index]=name} map for index in [_first_item, _last_item].
    -- Fully pcall-protected: any unexpected enum shape (missing markers,
    -- userdata that doesn't tostring) yields {} instead of aborting the
    -- whole script.
    local t = {}
    if not e then return t end
    local ok, first, last = pcall(function() return e._first_item, e._last_item end)
    if not ok or type(first) ~= 'number' or type(last) ~= 'number' then return t end
    for i = first, last do
        local ok2, v = pcall(function() return e[i] end)
        if ok2 and v ~= nil then t[tostring(i)] = tostring(v) end
    end
    return t
end

local function dump_enum_array(e)
    -- Same data as dump_enum but emitted as a dense array indexed from 0.
    local t = {}
    if not e then return t end
    local ok, first, last = pcall(function() return e._first_item, e._last_item end)
    if not ok or type(first) ~= 'number' or type(last) ~= 'number' then return t end
    for i = first, last do
        local ok2, v = pcall(function() return e[i] end)
        t[#t + 1] = (ok2 and v ~= nil) and tostring(v) or tostring(i)
    end
    return t
end

-- ---------------------------------------------------------------------------
-- happiness bucket (rough; DF stress thresholds shift across patches)
-- ---------------------------------------------------------------------------

local function happiness_bucket(stress)
    if not stress then return 'Unknown' end
    if stress <= -10000 then return 'Ecstatic'
    elseif stress <= -5000 then return 'Happy'
    elseif stress <= -1000 then return 'Content'
    elseif stress <=   1000 then return 'Fine'
    elseif stress <=  25000 then return 'Unhappy'
    elseif stress <=  50000 then return 'Very Unhappy'
    else                        return 'Miserable'
    end
end

-- ---------------------------------------------------------------------------
-- built-in role definitions (ported in spirit from DT defaults)
-- The UI reads these and computes 0-100 ratings per dwarf.
-- skills[].id is a job_skill enum index. attrs / traits are name strings.
-- ---------------------------------------------------------------------------

local function roles()
    -- Use string skill names; the UI resolves them to IDs via enums.skill.
    return {
        {
            id = 'Miner',
            skills = { { names = { 'MINING' }, weight = 1.0 } },
            attrs  = { phys = { 'STRENGTH', 'TOUGHNESS', 'ENDURANCE' }, ment = { 'SPATIAL_SENSE' } },
            traits = {},
        },
        {
            id = 'Woodcutter',
            -- DF 53.x emits the chopping skill as either WOOD_CUTTING or AXE
            -- (axe-as-tool); some forks use WOODCUTTING. Try all three.
            skills = { { names = { 'WOOD_CUTTING', 'WOODCUTTING', 'AXE' }, weight = 1.0 } },
            attrs  = { phys = { 'STRENGTH', 'AGILITY' }, ment = { } },
            traits = {},
        },
        {
            id = 'Carpenter',
            skills = { { names = { 'CARPENTRY' }, weight = 1.0 } },
            attrs  = { phys = { 'STRENGTH', 'AGILITY' }, ment = { 'CREATIVITY', 'SPATIAL_SENSE' } },
            traits = {},
        },
        {
            id = 'Mason',
            skills = { { names = { 'MASONRY' }, weight = 1.0 } },
            attrs  = { phys = { 'STRENGTH', 'AGILITY' }, ment = { 'CREATIVITY', 'SPATIAL_SENSE' } },
            traits = {},
        },
        {
            id = 'Mechanic',
            skills = { { names = { 'MECHANICS', 'MECHANIC' }, weight = 1.0 } },
            attrs  = { phys = { 'AGILITY' }, ment = { 'ANALYTICAL_ABILITY', 'SPATIAL_SENSE', 'KINESTHETIC_SENSE' } },
            traits = {},
        },
        {
            id = 'Doctor',
            skills = {
                { names = { 'DIAGNOSE' }, weight = 1.0 },
                { names = { 'SURGERY' },  weight = 0.8 },
                { names = { 'SUTURING' }, weight = 0.6 },
                { names = { 'SET_BONE', 'BONE_SETTING' }, weight = 0.6 },
                { names = { 'DRESS_WOUNDS', 'DRESSING_WOUNDS' }, weight = 0.5 },
            },
            attrs  = { phys = { 'AGILITY' }, ment = { 'ANALYTICAL_ABILITY', 'EMPATHY', 'KINESTHETIC_SENSE' } },
            traits = {},
        },
        {
            id = 'Farmer',
            skills = {
                { names = { 'PLANT', 'PLANT_GATHERING', 'GROWER' }, weight = 1.0 },
                { names = { 'HERBALISM' },                          weight = 0.5 },
                { names = { 'PROCESSPLANTS', 'PROCESS_PLANT' },     weight = 0.5 },
            },
            attrs  = { phys = { 'TOUGHNESS', 'ENDURANCE' }, ment = { 'PATIENCE' } },
            traits = {},
        },
        {
            id = 'Brewer',
            skills = { { names = { 'BREWING' }, weight = 1.0 } },
            attrs  = { phys = { 'AGILITY' }, ment = { 'ANALYTICAL_ABILITY', 'CREATIVITY' } },
            traits = {},
        },
        {
            id = 'Cook',
            skills = { { names = { 'COOK', 'COOKING' }, weight = 1.0 } },
            attrs  = { phys = { 'AGILITY' }, ment = { 'CREATIVITY', 'KINESTHETIC_SENSE' } },
            traits = {},
        },
        {
            id = 'Smelter',
            -- DF historically: SMELT. DF 50+/53.x sometimes exposes the smelter
            -- skill as FURNACE_OPERATOR. Accept either.
            skills = { { names = { 'SMELT', 'FURNACE_OPERATOR' }, weight = 1.0 } },
            attrs  = { phys = { 'STRENGTH', 'TOUGHNESS', 'ENDURANCE' }, ment = { } },
            traits = {},
        },
        {
            id = 'Smith',
            skills = {
                { names = { 'FORGE_WEAPON', 'WEAPONSMITH' },        weight = 1.0 },
                { names = { 'FORGE_ARMOR',  'ARMORSMITH' },         weight = 1.0 },
                { names = { 'FORGE_FURNITURE', 'BLACKSMITHING', 'BLACKSMITH' }, weight = 0.7 },
                { names = { 'METALCRAFT', 'METAL_CRAFT' },          weight = 0.5 },
            },
            attrs  = { phys = { 'STRENGTH', 'AGILITY' }, ment = { 'CREATIVITY', 'KINESTHETIC_SENSE', 'SPATIAL_SENSE' } },
            traits = {},
        },
        {
            id = 'Soldier (Melee)',
            skills = {
                { names = { 'MELEE_COMBAT' }, weight = 0.5 },
                { names = { 'AXE' },          weight = 1.0 },
                { names = { 'SWORD' },        weight = 1.0 },
                { names = { 'MACE' },         weight = 1.0 },
                { names = { 'HAMMER' },       weight = 1.0 },
                { names = { 'SPEAR' },        weight = 1.0 },
                { names = { 'FIGHTER' },      weight = 0.5 },
                { names = { 'ARMOR' },        weight = 0.6 },
                { names = { 'SHIELD' },       weight = 0.6 },
                { names = { 'DODGING' },      weight = 0.5 },
            },
            attrs  = { phys = { 'STRENGTH', 'AGILITY', 'TOUGHNESS', 'ENDURANCE' }, ment = { 'KINESTHETIC_SENSE', 'WILLPOWER' } },
            traits = {},
        },
    }
end

-- ---------------------------------------------------------------------------
-- per-unit accessors
-- ---------------------------------------------------------------------------

local function current_job_name(u)
    local n = try(function()
        if u.job and u.job.current_job then
            return dfhack.job.getName(u.job.current_job)
        end
    end, nil)
    if n and n ~= '' then return n end
    return 'No Job'
end

local function arrived_year(u)
    -- DF 50+ exposes a few candidate fields. Try them in order; fall back to birth_year.
    local y = try(function() return u.curse_year end, nil)
    -- The most reliable per-unit "arrived in fortress" timestamp DF stores is the
    -- creation/sighting year on the historical figure. Approximate via birth_year
    -- if nothing else is available.
    return y or u.birth_year or 0
end

local function migration_wave(u, cur_year)
    -- Group migrants by 1-year arrival buckets, indexed from the year of the
    -- earliest member of the population.
    local y = arrived_year(u) or cur_year or 0
    return y
end

local function attr_pair(rec)
    return {
        v   = try(function() return rec.value end, 0) or 0,
        max = try(function() return rec.max_value end, 0) or 0,
    }
end

local function unit_skills(u)
    local out = {}
    local skills = try(function() return u.status.current_soul.skills end, nil)
    if not skills then return out end
    for _, s in ipairs(skills) do
        out[#out + 1] = {
            id     = tonumber(s.id) or -1,
            rating = tonumber(s.rating) or 0,
            xp     = tonumber(s.experience) or 0,
            rust   = tonumber(s.rusty) or 0,
        }
    end
    return out
end

local function unit_attrs(u)
    -- Physical attrs live on the unit body (u.body.physical_attrs), mental
    -- attrs live on the soul (u.status.current_soul.mental_attrs). Reading
    -- physical from the soul yields nothing, which is why the page showed
    -- "? / ?" for every physical attribute.
    local phys, ment = {}, {}
    local pa = try(function() return u.body.physical_attrs end, nil)
                or try(function() return u.physical_attrs end, nil)
    local ma = try(function() return u.status.current_soul.mental_attrs end, nil)
    if pa then for _, r in ipairs(pa) do phys[#phys + 1] = attr_pair(r) end end
    if ma then for _, r in ipairs(ma) do ment[#ment + 1] = attr_pair(r) end end
    return phys, ment
end

local function unit_labors(u, last_labor)
    local out = {}
    for i = 0, last_labor do out[#out + 1] = u.status.labors[i] and true or false end
    return out
end

local function unit_traits(u)
    local out = {}
    local t = try(function() return u.status.current_soul.personality.traits end, nil)
    if not t then return out end
    for i = 0, #t - 1 do
        out[#out + 1] = { id = i, v = tonumber(t[i]) or 50 }
    end
    return out
end

local function unit_needs(u)
    local out = {}
    local n = try(function() return u.status.current_soul.personality.needs end, nil)
    if not n then return out end
    for _, rec in ipairs(n) do
        out[#out + 1] = {
            id    = tonumber(rec.id) or -1,
            focus = tonumber(rec.focus_level) or 0,
            level = tonumber(rec.need_level) or 0,
        }
    end
    return out
end

local function unit_prefs(u)
    -- Coarse summary: list each preference's enum-type token. The browser
    -- formats user-friendly strings; richer details (specific item/creature
    -- IDs) are intentionally not resolved here.
    local out = {}
    local prefs = try(function() return u.status.current_soul.personality.preferences end, nil)
    if not prefs then return out end
    for _, p in ipairs(prefs) do
        local t = tostring(p.type) or ''
        out[#out + 1] = t
    end
    return out
end

local function unit_health(u)
    local h = {
        wounds        = try(function() return #u.body.wounds end, 0) or 0,
        missing_limbs = 0,
        hunger        = try(function() return u.counters2.hunger_timer end, 0) or 0,
        thirst        = try(function() return u.counters2.thirst_timer end, 0) or 0,
        tired         = try(function() return u.counters2.sleepiness_timer end, 0) or 0,
        paralysis     = try(function() return u.counters.paralysis end, 0) or 0,
        unconscious   = try(function() return u.counters.unconscious end, 0) or 0,
        pain          = try(function() return u.counters.pain end, 0) or 0,
    }
    -- Count missing/severed body components if exposed by this DF version.
    local components = try(function() return u.body.components end, nil)
    if components and components.body_part_status then
        for _, st in ipairs(components.body_part_status) do
            if st.missing or st.gone then h.missing_limbs = h.missing_limbs + 1 end
        end
    end
    return h
end

local function unit_squad(u)
    local sq = { id = -1, name = '' }
    local sid = try(function() return u.military.squad_id end, -1) or -1
    sq.id = sid
    if sid and sid ~= -1 then
        local s = try(function() return df.squad.find(sid) end, nil)
        if s then sq.name = df2utf(try(function() return dfhack.military.getSquadName(s) end, '') or '') end
    end
    return sq
end

local function legendary_status(skills)
    for _, s in ipairs(skills) do
        if (s.rating or 0) >= 15 then return true end
    end
    return false
end

local function moodable_skill(u)
    return df2utf(try(function() return dfhack.units.getMoodSkillName and dfhack.units.getMoodSkillName(u) or '' end, '') or '')
end

-- ---------------------------------------------------------------------------
-- main
--
-- Wrapped in a top-level pcall: any structural surprise on a future DF/DFHack
-- patch becomes an in-band {error: "..."} JSON document instead of a script
-- abort that surfaces in the proxy as the bare locale warning from glibc and
-- the unhelpful 503. The browser already handles a doc with units = [].
-- ---------------------------------------------------------------------------

local function build_doc()
    local last_labor = try(function() return df.unit_labor._last_item end, 0) or 0

    local enums = {
        labor       = dump_enum(df.unit_labor),
        skill       = dump_enum(df.job_skill),
        attr_phys   = dump_enum_array(df.physical_attribute_type),
        attr_ment   = dump_enum_array(df.mental_attribute_type),
        trait       = dump_enum(df.personality_facet_type),
        need        = dump_enum(df.need_type),
    }

    local function build_unit(u)
        local skills = try(function() return unit_skills(u) end, {}) or {}
        local phys, ment = {}, {}
        local ok_attrs, p, m = pcall(unit_attrs, u)
        if ok_attrs then phys, ment = p or {}, m or {} end

        local stress = try(function() return u.status.current_soul.personality.stress end, nil)
        local nick = df2utf(try(function() return u.name.nickname end, '') or '')
        local first = df2utf(try(function() return u.name.first_name end, '') or '')
        local custom_prof = df2utf(try(function() return u.custom_profession end, '') or '')
        local race_name = df2utf(try(function()
            local r = df.global.world.raws.creatures.all[u.race]
            return r and r.creature_id or ''
        end, '') or '')
        local caste_name = df2utf(try(function()
            local r = df.global.world.raws.creatures.all[u.race]
            return r and r.caste[u.caste].caste_id or ''
        end, '') or '')

        return {
            id                = u.id,
            hist_id           = try(function() return u.hist_figure_id end, -1) or -1,
            name              = df2utf(try(function() return dfhack.units.getReadableName(u) end, '') or ''),
            first_name        = first,
            nickname          = nick,
            profession        = df2utf(try(function() return dfhack.units.getProfessionName(u) end, '') or ''),
            custom_profession = custom_prof,
            race              = race_name,
            caste             = caste_name,
            birth_year        = try(function() return u.birth_year end, 0) or 0,
            arrived_year      = arrived_year(u),
            wave              = migration_wave(u, df.global.cur_year),
            current_job       = df2utf(current_job_name(u)),
            stress            = stress,
            happiness         = happiness_bucket(stress),
            legendary         = legendary_status(skills),
            moodable_skill    = moodable_skill(u),
            phys_attrs        = phys,
            ment_attrs        = ment,
            skills            = skills,
            labors            = try(function() return unit_labors(u, last_labor) end, {}) or {},
            traits            = try(function() return unit_traits(u) end, {}) or {},
            preferences       = try(function() return unit_prefs(u) end, {}) or {},
            needs             = try(function() return unit_needs(u) end, {}) or {},
            health            = try(function() return unit_health(u) end, {}) or {},
            squad             = try(function() return unit_squad(u) end, { id = -1, name = '' }) or { id = -1, name = '' },
        }
    end

    local units = {}
    local errors = {}
    for _, u in ipairs(df.global.world.units.active) do
        local ok_filter, is_cz = pcall(function() return dfhack.units.isCitizen(u) and dfhack.units.isAlive(u) end)
        if ok_filter and is_cz then
            local ok, unit_or_err = pcall(build_unit, u)
            if ok then
                units[#units + 1] = unit_or_err
            else
                errors[#errors + 1] = { id = (try(function() return u.id end, -1) or -1), reason = tostring(unit_or_err) }
            end
        end
    end

    -- Work Details snapshot. DF 53.x's Work Details system reasserts each
    -- unit's status.labors[] from these every tick — so the page can read
    -- the labor flags but cannot meaningfully *toggle* them; the next tick
    -- overwrites whatever we wrote. We expose the WD list so the UI can
    -- annotate each labor cell with the controlling detail and explain why
    -- in-page edits don't stick.
    local work_details = {}
    local wds = try(function() return df.global.plotinfo.labor_info.work_details end, nil)
    if wds then
        for i = 0, #wds - 1 do
            local wd = wds[i]
            local rec = {
                idx    = i,
                name   = df2utf(try(function() return wd.name end, '') or ''),
                icon   = try(function() return wd.icon end, 0) or 0,
                labors = {},   -- list of labor IDs whose allowed_labors[id] is true
                units  = {},   -- list of assigned unit IDs
                flags  = {},   -- bit flags as a {name=true} map (best-effort)
            }
            local al = try(function() return wd.allowed_labors end, nil)
            if al then for j = 0, last_labor do if al[j] then rec.labors[#rec.labors + 1] = j end end end
            local au = try(function() return wd.assigned_units end, nil)
            if au then for _, uid in ipairs(au) do rec.units[#rec.units + 1] = uid end end
            local fl = try(function() return wd.flags end, nil)
            if fl then
                for k, v in pairs(fl) do
                    if type(k) == 'string' and not k:match('^_') then rec.flags[k] = v and true or false end
                end
            end
            work_details[#work_details + 1] = rec
        end
    end

    return {
        year    = try(function() return df.global.cur_year end, 0) or 0,
        version = {
            df     = try(function() return dfhack.getDFVersion() end, '') or '',
            dfhack = try(function() return dfhack.getDFHackVersion() end, '') or '',
        },
        enums   = enums,
        roles   = roles(),
        units   = units,
        work_details = work_details,
        unit_errors  = errors,
    }
end

local ok, doc = pcall(build_doc)
if ok then
    print(json.encode(doc))
else
    print(json.encode({
        error = tostring(doc),
        year  = 0,
        enums = { labor = {}, skill = {}, attr_phys = {}, attr_ment = {}, trait = {}, need = {} },
        roles = {},
        units = {},
    }))
end
